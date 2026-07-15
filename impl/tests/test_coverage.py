"""Cover the complete command contract, operation ordering, transports, and adversarial edges."""

import contextlib
import http.client
import json
import os
import socket
import time
from http.server import ThreadingHTTPServer

import pytest

from mesoSPIM.src.mesoSPIM_RemoteControl_Dispatcher import (
    run,
    complete,
    fail,
    operation_snapshot,
    error_info,
    COMMANDS,
    jsonable,
    precheck,
    ValidationError,
    BusyError,
    UnknownCommand,
    READ,
    EMERGENCY,
)
from mesoSPIM.src.mesoSPIM_RemoteControl_Config import (
    MILESTONE_FINISHED,
    MILESTONE_TIMELAPSE,
    MCP_SERVER_NAME,
    MCP_PROTOCOL_VERSION,
    OK_MARKER,
    MAX_ACQUISITION_PLANES,
)
from mesoSPIM.src import mesoSPIM_RemoteControl_Commands as commands
from mesoSPIM.src import mesoSPIM_RemoteControl_Servers as servers
from fakes import FakeCore, defer_recorder


def _acquisition(planes=1, **overrides):
    """Return a minimal row whose declared plane count matches its z geometry."""
    row = {"z_start": 0, "z_end": planes - 1, "z_step": 1, "planes": planes}
    row.update(overrides)
    return row


# ----- a contracts-style sweep: every command reaches the right Core call -----

# {command: (args, expected Core call/emit name)}. Commands that only mutate state (no Core call)
# are exercised elsewhere and omitted here.
_EXPECTED = {
    "move_absolute": ({"targets": {"x": 1}}, "move_absolute"),
    "move_relative": ({"deltas": {"x": 1}}, "move_relative"),
    "zero": ({}, "zero_axes"),
    "unzero": ({}, "unzero_axes"),
    "stop": ({}, "emit:sig_stop_movement"),
    "close_shutters": ({}, "close_shutters"),
    "time_lapse_stop": ({}, "stop_time_lapse"),
    "set_state": ({"settings": {"intensity": 10}}, "state_request_handler"),
    "set_filter": ({"filter": "Empty"}, "set_filter"),
    "set_zoom": ({"zoom": "1x"}, "set_zoom"),
    "set_laser": ({"laser": "488 nm"}, "set_laser"),
    "set_intensity": ({"intensity": 10}, "set_intensity"),
    "set_shutterconfig": ({"shutterconfig": "Both"}, "set_shutterconfig"),
    "set_camera": ({"camera_exposure_time": 0.02}, "state_request_handler"),
    "set_etl": ({"etl_l_offset": 1}, "state_request_handler"),
    "set_galvo": ({"galvo_l_frequency": 1}, "state_request_handler"),
    "set_laser_timing": ({"laser_l_delay_%": 1}, "state_request_handler"),
    "reload_etl_config": ({}, "emit:sig_state_request_and_wait_until_done"),
    "update_etl_from_laser": ({}, "emit:sig_state_request_and_wait_until_done"),
    "update_etl_from_zoom": ({}, "emit:sig_state_request_and_wait_until_done"),
    "save_etl_config": ({}, "emit:sig_save_etl_config"),
    "open_shutters": ({}, "open_shutters"),
    "start_live": ({}, "set_state"),
    "start_visual_mode": ({}, "set_state"),
    "start_lightsheet_alignment_mode": ({}, "set_state"),
    "load_sample": ({}, "move_absolute"),
    "unload_sample": ({}, "move_absolute"),
    "center_sample": ({}, "move_absolute"),
    "run_acquisition_list": ({}, "start"),
    "run_selected_acquisition": ({}, "start"),
    "preview_acquisition": ({}, "preview_acquisition"),
    "acquire_start": ({"acquisition": _acquisition()}, "start"),
    "time_lapse_start": ({}, "run_time_lapse"),
}


@pytest.mark.parametrize("name,args,expected", [(n, a, e) for n, (a, e) in _EXPECTED.items()])
def test_command_reaches_expected_core_call(name, args, expected):
    core = FakeCore()
    run(core, name, args)  # immediate singleShot fires WAIT bodies
    made = [c[0] for c in core.calls]
    assert expected in made, f"{name}: expected {expected!r}, got {made}"


# Pin the safety-relevant command kinds independently of the wire matrix. Otherwise an accidental
# kind change could alter both production dispatch and the derived test expectation together.
_READ_COMMANDS = {
    "hello",
    "ping",
    "get_state",
    "get_position",
    "get_state_all",
    "get_config",
    "get_info",
    "get_limits",
    "get_capabilities",
    "get_manual",
    "get_progress",
    "self_test",
    "get_acquisition_list",
    "stat_files",
    "get_disk_space",
    "check_motion_limits",
}
_EMERGENCY_COMMANDS = {"stop", "stop_activity", "close_shutters", "time_lapse_stop", "clear_stuck_operation"}


def test_command_kinds_are_pinned():
    assert {n for n, c in COMMANDS.items() if c.kind == READ} == _READ_COMMANDS
    assert {n for n, c in COMMANDS.items() if c.kind == EMERGENCY} == _EMERGENCY_COMMANDS


@pytest.mark.parametrize("name", sorted(COMMANDS))
def test_every_command_rejects_an_unexpected_argument(name):
    core = FakeCore()
    with pytest.raises(ValidationError, match="unknown argument"):
        run(core, name, {"__unexpected__": True})
    assert core.calls == []
    assert core._remote_session["counter"] == 0


# ----- acquire_finish stash-and-restore (the patch-critical regression) -----


def test_acquire_start_then_finish_restores_exact_object():
    core = FakeCore(acq_list=["ORIG"])
    original = core.state["acq_list"]
    run(core, "acquire_start", {"acquisition": _acquisition()})
    assert core.state["acq_list"] is not original
    assert core._remote_session["prev_acq_list"] is original
    assert core.state["state"] == "idle"  # synchronous Core.start returned to idle
    complete(core, MILESTONE_FINISHED)  # acquire_start is a WAIT; clear the gate
    run(core, "acquire_finish", {})
    assert core.state["acq_list"] is original
    assert "prev_acq_list" not in core._remote_session


def test_standalone_acquire_finish_leaves_list_alone():
    core = FakeCore(acq_list=["KEEP"])
    original = core.state["acq_list"]
    run(core, "acquire_finish", {})  # no acquire_start ran
    assert core.state["acq_list"] is original  # not clobbered to None


def test_repeated_finish_does_not_restore_stale():
    core = FakeCore(acq_list=["ORIG"])
    run(core, "acquire_start", {"acquisition": _acquisition()})
    complete(core, MILESTONE_FINISHED)
    run(core, "acquire_finish", {})
    core.state["acq_list"] = ["NEW"]
    run(core, "acquire_finish", {})  # nothing saved -> leaves NEW alone
    assert core.state["acq_list"] == ["NEW"]


# ----- camera-dimension errors, pre-gate -----


@pytest.mark.parametrize(
    "camera_parameters", [{}, {"x_pixels": 2048}, {"x_pixels": "wide", "y_pixels": 2048}, {"y_pixels": None}]
)
@pytest.mark.parametrize(
    "name,args", [("get_config", {}), ("acquire_start", {"acquisition": _acquisition()})]
)
def test_camera_dimension_errors(camera_parameters, name, args):
    core = FakeCore()
    core.cfg.camera_parameters = camera_parameters
    with pytest.raises(ValidationError, match="camera_parameters"):
        run(core, name, args)
    assert core._remote_session["counter"] == 0  # rejected before the gate
    assert core.calls == []


# ----- ETL happy path (needs the seeded readback keys) -----


def test_etl_happy_path_returns_readback():
    core = FakeCore()
    reply = run(core, "update_etl_from_laser", {})
    assert reply["accepted"] is True
    readback = reply["operation"]["result"]
    assert "etl_l_amplitude" in readback and "etl_r_offset" in readback
    assert (
        "emit:sig_state_request_and_wait_until_done",
        ({"set_etls_according_to_laser": "488 nm"},),
        {},
    ) in [(c[0], c[1], c[2]) for c in core.calls]


def test_etl_empty_string_arg_is_rejected():
    core = FakeCore()
    with pytest.raises(ValidationError, match="path is required"):
        run(core, "reload_etl_config", {"path": ""})  # explicit empty must not fall back to state


# ----- stop_activity idle-guard (both branches) -----


def test_stop_activity_idle_does_not_call_stop():
    core = FakeCore(state="idle")
    run(core, "stop_activity", {})
    assert core.calls == []  # no double-fire while already idle


def test_stop_activity_running_calls_core_stop():
    core = FakeCore(state="live")
    run(core, "stop_activity", {})
    assert ("stop", (), {}) in [(c[0], c[1], c[2]) for c in core.calls]


# ----- row bounds / selected_row -----


def test_selected_row_checked_against_new_list():
    core = FakeCore()
    run(core, "set_acquisition_list", {"acquisitions": [_acquisition()], "selected_row": 0})
    assert core.state["selected_row"] == 0
    with pytest.raises(ValidationError):
        run(core, "set_acquisition_list", {"acquisitions": [_acquisition()], "selected_row": 5})


def test_set_acquisition_list_rejects_an_empty_gui_table():
    with pytest.raises(ValidationError, match="at least one row"):
        run(FakeCore(), "set_acquisition_list", {"acquisitions": []})


def test_set_acquisition_list_publishes_the_same_list_to_the_gui_bridge():
    class Bridge:
        def __init__(self):
            self.events = []

        def emit(self, *args):
            self.events.append(args)

    core = FakeCore()
    bridge = Bridge()
    core._remote_control_acquisition_list_signal = bridge
    run(core, "set_acquisition_list", {"acquisitions": [_acquisition()], "selected_row": 0})
    ((published, selected),) = bridge.events
    assert published is core.state["acq_list"]
    assert selected == 0


def test_run_selected_row_bounds():
    core = FakeCore()
    run(core, "run_selected_acquisition", {"row": 0})
    core.state["acq_list"] = ["a"]
    with pytest.raises(ValidationError):
        run(core, "run_selected_acquisition", {"row": 5})


@pytest.mark.parametrize(
    "name,args",
    [
        ("run_acquisition_list", {}),
        ("run_selected_acquisition", {"row": 0}),
        ("preview_acquisition", {"row": 0}),
        ("time_lapse_start", {"timepoints": 1, "interval_sec": 0}),
    ],
)
def test_acquisition_operations_reject_an_empty_installed_list(name, args):
    core = FakeCore(acq_list=[])
    with pytest.raises(ValidationError, match="acquisition list is empty"):
        run(core, name, args)
    assert core.calls == [] and core._remote_session["counter"] == 0


# ----- read documents: shapes and the no-.get() contract -----


def test_get_limits_enforced_axes():
    doc = run(FakeCore(), "get_limits", {})
    assert doc["enforced"]["axes"]["x"] == [-25000.0, 25000.0]
    assert doc["enforced"]["axes"]["theta"] == [-999.0, 999.0]
    assert doc["enforced"]["parameters"]["intensity"]["range"] == [0, 100]


def test_get_config_document_shape():
    doc = run(FakeCore(), "get_config", {})
    assert doc["camera"] == {"pixels_x": 2048, "pixels_y": 2048}
    assert {"name": "488 nm", "wavelength_nm": 488} in doc["lasers"]
    assert doc["axes"] == ["x", "y", "z", "f", "theta"]


def test_get_capabilities_has_position_keys_and_setting_groups():
    doc = run(FakeCore(), "get_capabilities", {})
    assert doc["position_keys"]["x"] == "x_pos"
    assert "set_camera" in doc["setting_groups"]


def test_get_info_reports_warnings_and_operation():
    doc = run(FakeCore(), "get_info", {})
    assert doc["warnings"] == []
    assert doc["operation"] == {"status": "idle"}


def test_get_state_all_unknown_key_is_a_validation_error():
    core = FakeCore()
    with pytest.raises(ValidationError):
        run(core, "get_state_all", {"keys": ["does_not_exist"]})
    # The client supplied the unknown key, so the transport classifies it as validation.
    try:
        run(core, "get_state_all", {"keys": ["does_not_exist"]})
    except ValidationError as error:
        assert error_info(error)[0] == "validation"


# ----- faithful WAIT ordering (deferred body, not the immediate shim) -----


def test_wait_stays_processing_until_body_and_signal():
    core = FakeCore()
    with defer_recorder() as pending:
        reply = run(core, "start_live", {})
        assert reply["operation"]["status"] == "processing"
        assert core.calls == []  # body not run yet (deferred)
        assert len(pending) == 1
        pending.pop(0)()  # enter the registered WAIT handler
        pending.pop(0)()  # fire the deferred Core call
    assert ("set_state", ("live",), {}) in [(c[0], c[1], c[2]) for c in core.calls]
    assert operation_snapshot(core)["status"] == "processing"
    core.state["state"] = "idle"  # Core.stop runs before the real terminal signal
    complete(core, MILESTONE_FINISHED)  # simulate sig_finished
    assert operation_snapshot(core)["status"] == "completed"


def test_stage_move_returns_processing_then_completes_from_readback():
    core = FakeCore()
    with defer_recorder() as pending:
        reply = run(core, "move_absolute", {"targets": {"x": 100}})
        assert reply["operation"] == {
            "id": "op-000001",
            "command": "move_absolute",
            "status": "processing",
        }
        assert core.calls == []
        assert run(core, "get_progress", {})["operation"]["status"] == "processing"

        pending.pop(0)()  # enter the registered WAIT handler
        assert operation_snapshot(core)["target"] == {"x": 100.0}
        pending.pop(0)()  # issue the non-blocking stage command
        assert core.calls == [("move_absolute", ({"x_abs": 100.0},), {"wait_until_done": False})]
        assert operation_snapshot(core)["status"] == "processing"

        pending.pop(0)()  # poll the updated position readback

    assert operation_snapshot(core) == {
        "id": "op-000001",
        "command": "move_absolute",
        "status": "completed",
        "target": {"x": 100.0},
        "observed": {"x": 100.0},
        "result": {"target": {"x": 100.0}},
    }


def test_stopped_stage_move_fails_before_claiming_success():
    core = FakeCore()
    with defer_recorder() as pending:
        run(core, "move_absolute", {"targets": {"x": 100}})
        pending.pop(0)()  # enter movement handler
        pending.pop(0)()  # issue movement; leave readback pending
        run(core, "stop", {})
        pending.pop(0)()
    operation = operation_snapshot(core)
    assert operation["status"] == "failed"
    assert operation["stop_requested"] is True
    assert "stopped before the target" in operation["error"]


def test_stage_move_stays_busy_until_readback_reaches_target():
    core = FakeCore()

    def issue_without_readback(targets, wait_until_done=False):
        core._record("move_absolute", targets, wait_until_done=wait_until_done)

    core.move_absolute = issue_without_readback
    with defer_recorder() as pending:
        run(core, "move_absolute", {"targets": {"x": 100}})
        pending.pop(0)()  # enter movement handler
        pending.pop(0)()  # issue move
        pending.pop(0)()  # first readback still says x=0
        operation = operation_snapshot(core)
        assert operation["status"] == "processing"
        assert operation["observed"] == {"x": 0.0}
        assert len(pending) == 1  # another readback check was scheduled
        with pytest.raises(BusyError):
            run(core, "set_intensity", {"intensity": 20})


def test_preset_completion_uses_physical_position_readback():
    core = FakeCore()
    core.state["position"]["y_pos"] = 5.0  # simulated software-zero offset

    def issue_physical_move(targets, wait_until_done=False, use_internal_position=True):
        core._record(
            "move_absolute",
            targets,
            wait_until_done=wait_until_done,
            use_internal_position=use_internal_position,
        )
        core.state["position_absolute"]["y_pos"] = targets["y_abs"]
        core.state["position"]["y_pos"] = targets["y_abs"] + 5.0

    core.move_absolute = issue_physical_move
    with defer_recorder() as pending:
        run(core, "load_sample", {})
        pending.pop(0)()  # enter movement handler
        pending.pop(0)()  # issue move
        pending.pop(0)()  # confirm physical readback
    operation = operation_snapshot(core)
    assert operation["status"] == "completed"
    assert operation["observed"] == {"y": 45000.0}


def test_deferred_body_failure_marks_op_failed():
    core = FakeCore()

    def boom(**_):
        raise RuntimeError("hardware refused")

    core.preview_acquisition = boom
    with defer_recorder() as pending:
        run(core, "preview_acquisition", {"row": 0})
        pending.pop(0)()  # enter registered WAIT handler
        pending.pop(0)()  # execute preview body
    assert operation_snapshot(core)["status"] == "failed"
    assert "hardware refused" in core._remote_session["operation"]["error"]


# ----- TCP auth + dispatch path -----


class _Acc:
    def __init__(self, core):
        self.core = core

    def dispatch(self, name, args):
        return run(self.core, name, args)


class _Conn:
    def __init__(self):
        self.written = []

    def write(self, data):
        self.written.append(bytes(data))

    def flush(self):
        pass

    def disconnectFromHost(self):
        pass

    def deleteLater(self):
        pass


def _make_tcp(core, token):
    adapter = servers.TcpAdapter()
    adapter._acceptor = _Acc(core)
    adapter._token = token or None
    adapter._clients = {}
    return adapter


def _last_frame(conn):
    _, _, rest = conn.written[-1].partition(b"\n")
    return rest.decode()


def test_tcp_auth_wrong_token_fails_and_drops():
    adapter, conn = _make_tcp(FakeCore(), "secret"), _Conn()
    adapter._clients[conn] = {"decoder": servers.FrameDecoder(), "authed": False}
    adapter._handle(conn, "WRONG")
    assert _last_frame(conn) == "AUTH-FAILED"
    assert conn not in adapter._clients  # dropped


def test_tcp_auth_then_dispatch_success():
    core = FakeCore()
    adapter, conn = _make_tcp(core, "secret"), _Conn()
    adapter._clients[conn] = {"decoder": servers.FrameDecoder(), "authed": False}
    adapter._handle(conn, "secret")
    assert _last_frame(conn) == "OK"
    adapter._handle(conn, json.dumps({"get_position": {}}))
    reply = _last_frame(conn)
    assert reply.startswith(OK_MARKER)
    assert json.loads(reply[len(OK_MARKER) :]) == {"x": 0.0, "y": 0.0, "z": 0.0, "f": 0.0, "theta": 0.0}


def test_tcp_validation_error_carries_code():
    adapter, conn = _make_tcp(FakeCore(), None), _Conn()  # no token -> pre-authed
    adapter._clients[conn] = {"decoder": servers.FrameDecoder(), "authed": True}
    adapter._handle(conn, json.dumps({"move_absolute": {"targets": {"x": 999999}}}))
    reply = _last_frame(conn)
    assert reply.startswith("error: [validation]")
    assert (
        "x=999999.0 is outside the allowed range [-25000.0, 25000.0]" in reply
    )  # the valid range is reported back


# ----- framing edges -----


def test_frame_rejects_oversized():
    with pytest.raises(servers.FramingError):
        servers.frame(b"x" * (servers.config.MAX_FRAME_BYTES + 1))


@pytest.mark.parametrize("bad", [b"abc\n", b"-5\n", b"+1\n", b"1 0\n", (b"9" * 20) + b"\n"])
def test_decoder_rejects_bad_headers(bad):
    decoder = servers.FrameDecoder()
    decoder.feed(bad + b"payload")
    with pytest.raises(servers.FramingError):
        list(decoder.frames())


def test_decoder_reassembles_empty_and_joined():
    decoder = servers.FrameDecoder()
    decoder.feed(servers.frame(b"") + servers.frame(b"abc") + servers.frame(b"hi"))
    assert list(decoder.frames()) == [b"", b"abc", b"hi"]


def test_decoder_rejects_huge_length_without_allocating():
    decoder = servers.FrameDecoder()
    decoder.feed(b"999999999\n")  # ~1 GB claimed; must reject on the header
    with pytest.raises(servers.FramingError):
        list(decoder.frames())


# ----- adversarial: a refused call touches nothing -----


@pytest.mark.parametrize(
    "name,args",
    [
        ("move_absolute", {"targets": {"x": True}}),  # bool is not a number
        ("move_absolute", {"targets": {"nope": 1}}),  # unknown axis
        ("set_filter", {"filter": 1}),  # wrong type
        ("set_intensity", {"intensity": "50"}),  # string not number
        ("set_state", {"settings": {"x_max": 1}}),  # non-settable key
        ("time_lapse_start", {"timepoints": True}),  # bool not int
        ("acquire_start", {"acquisition": {"filter": "NOPE"}}),
    ],
)
def test_refused_call_touches_nothing(name, args):
    core = FakeCore()
    with pytest.raises((ValidationError, BusyError)):
        run(core, name, args)
    assert core.calls == []
    assert core._remote_session["counter"] == 0


# ----- strengthened MCP + self_test assertions -----


def test_mcp_initialize_exact():
    init = servers._mcp_reply(_Acc(FakeCore()), {"jsonrpc": "2.0", "id": 1, "method": "initialize"})
    info = init["result"]["serverInfo"]
    assert info["name"] == MCP_SERVER_NAME
    assert init["result"]["protocolVersion"] == MCP_PROTOCOL_VERSION


def test_self_test_reports_move_count_and_all_pass():
    ok, report = commands.self_test(FakeCore())
    assert ok is True
    assert all(line.startswith("PASS") for line in report)
    assert any("in-range move(s) reached the mock stage" in line for line in report)


# ----- milestone routing: a completion signal only resolves the op that asked for it -----


def test_complete_ignores_wrong_milestone():
    core = FakeCore()
    run(core, "start_live", {})  # milestone = finished
    complete(core, MILESTONE_TIMELAPSE)  # a stray time_lapse milestone must not resolve it
    assert operation_snapshot(core)["status"] == "processing"
    core.state["state"] = "idle"
    complete(core, MILESTONE_FINISHED)  # the matching one does
    assert operation_snapshot(core)["status"] == "completed"


def test_fail_ignores_wrong_milestone():
    core = FakeCore()
    run(core, "start_live", {})  # milestone = finished
    fail(core, MILESTONE_TIMELAPSE, RuntimeError("other op"))
    assert operation_snapshot(core)["status"] == "processing"
    fail(core, MILESTONE_FINISHED, RuntimeError("boom"))
    assert operation_snapshot(core)["status"] == "failed"


# ----- unknown command classified as unknown_command end-to-end, not execution -----


def test_run_unknown_command_is_classified():
    core = FakeCore()
    with pytest.raises(UnknownCommand):
        run(core, "does_not_exist", {})
    try:
        run(core, "does_not_exist", {})
    except Exception as error:
        assert error_info(error)[0] == "unknown_command"


def test_unknown_command_over_tcp_carries_code():
    adapter, conn = _make_tcp(FakeCore(), None), _Conn()  # no token -> pre-authed
    adapter._clients[conn] = {"decoder": servers.FrameDecoder(), "authed": True}
    adapter._handle(conn, json.dumps({"no_such_command": {}}))
    assert _last_frame(conn).startswith("error: [unknown_command]")


# ----- preview decides its own completion: complete iff back to idle, else fail -----


def test_preview_completes_only_when_idle():
    core = FakeCore(state="idle")  # body runs, sees idle -> complete
    run(core, "preview_acquisition", {"row": 0})
    assert operation_snapshot(core)["status"] == "completed"


def test_preview_fails_if_not_idle():
    core = FakeCore(state="live")  # body runs, sees non-idle -> fail
    run(core, "preview_acquisition", {"row": 0})
    assert operation_snapshot(core)["status"] == "failed"


# ----- busy gate: a WAIT operation blocks the next mutation -----


def test_busy_gate_blocks_second_mutation():
    core = FakeCore()
    run(core, "start_live", {})  # WAIT op stays processing (no signal offline)
    with pytest.raises(BusyError):
        run(core, "move_absolute", {"targets": {"x": 1}})
    assert core._remote_session["operation"]["command"] == "start_live"  # untouched
    assert ("move_absolute", ({"x": 1},), {"wait_until_done": False}) not in [
        (c[0], c[1], c[2]) for c in core.calls
    ]  # the blocked move never reached the Core


# precheck() rejects an unknown name on the calling thread. Shape, limits, and busy state remain
# Core-thread decisions in run(), with the busy check last.


def test_precheck_rejects_unknown_command_fast():
    with pytest.raises(UnknownCommand, match="does_not_exist"):
        precheck("does_not_exist")  # a wrong name fails fast on the calling thread
    assert error_info(UnknownCommand("x"))[0] == "unknown_command"


def test_precheck_passes_a_known_command():
    assert precheck("move_absolute") is None  # a known name proceeds to run() for the rest


# ----- validation errors take precedence over busy -----


def test_busy_is_checked_after_validation():
    core = FakeCore()
    run(core, "start_live", {})  # a WAIT holds the gate

    # Invalid arguments receive a useful validation error even while another operation is active.
    with pytest.raises(ValidationError, match="valid axes"):
        run(core, "move_absolute", {"targets": {"nope": 1}})  # bad shape -> validation, not busy
    with pytest.raises(ValidationError, match="allowed range"):
        run(core, "move_absolute", {"targets": {"x": 999999}})  # out of range -> validation, not busy

    # Only an otherwise valid mutation is rejected as busy.
    with pytest.raises(BusyError, match="start_live"):
        run(core, "move_absolute", {"targets": {"x": 1}})

    # an unknown name still wins over busy (permanent, checked first)
    with pytest.raises(UnknownCommand):
        run(core, "does_not_exist", {})
    assert core.calls == [("set_state", ("live",), {})]  # nothing new reached the Core throughout


# The lock must preserve one-operation admission even if callers reach run() concurrently. A slow
# session read widens the race window so this test fails reliably if the lock is removed.


class _SlowSession(dict):
    def __getitem__(self, key):
        value = super().__getitem__(key)
        if key == "operation":
            time.sleep(0.003)
        return value


def test_concurrent_mutations_exactly_one_passes_the_gate():
    import threading

    core = FakeCore()
    core._remote_session = _SlowSession(core._remote_session)
    workers = 24
    ready = threading.Barrier(workers)
    outcomes = []
    record = threading.Lock()

    def hammer():
        ready.wait()  # release together, maximise contention
        try:
            run(core, "start_live", {})  # WAIT: the winner holds the gate open
            verdict = "opened"
        except BusyError:
            verdict = "busy"
        with record:
            outcomes.append(verdict)

    threads = [threading.Thread(target=hammer) for _ in range(workers)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()

    assert outcomes.count("opened") == 1  # exactly one mutation was ever accepted
    assert outcomes.count("busy") == workers - 1  # every other was refused, none slipped through
    assert core._remote_session["counter"] == 1  # only one op was ever minted (no double-begin)
    assert operation_snapshot(core)["command"] == "start_live"


# ----- the grouped-setter uniformity fix: a typo'd key is rejected, not silently dropped -----


def test_grouped_setter_rejects_unknown_key():
    core = FakeCore()
    with pytest.raises(ValidationError):
        run(core, "set_camera", {"camera_exposure_time": 0.02, "bogus_typo": 999})
    assert core.calls == []  # nothing partially applied


def test_acq_check_rejects_non_list():
    core = FakeCore()
    with pytest.raises(ValidationError):  # not a raw TypeError -> code 'execution'
        run(core, "get_disk_space", {"acquisitions": 5})


def test_stat_files_rejects_non_list():
    core = FakeCore()
    with pytest.raises(ValidationError):  # a bare string must not iterate into chars
        run(core, "stat_files", {"files": "abc"})


def test_acq_check_null_uses_loaded_list():
    core = FakeCore()  # explicit JSON null -> fall back, not a crash
    reply = run(core, "get_disk_space", {"acquisitions": None})
    assert reply["free_bytes"] == 1_000_000


@pytest.mark.parametrize("name", ["get_disk_space", "check_motion_limits"])
def test_acquisition_checks_reject_an_empty_list(name):
    with pytest.raises(ValidationError, match="acquisition list is required"):
        run(FakeCore(), name, {"acquisitions": []})


# ----- the real Acceptor: its signal wiring is the only path from a Qt signal to complete() -----


def test_acceptor_finished_signal_completes_op():
    core = FakeCore()
    acceptor = servers.Acceptor(core)
    acceptor.dispatch("start_live", {})  # WAIT op, milestone finished
    assert operation_snapshot(core)["status"] == "processing"
    core.state["state"] = "idle"  # Core.stop precedes the real mode-completion signal
    core.sig_finished.emit()  # the wiring must route this to complete()
    assert operation_snapshot(core)["status"] == "completed"


def test_acceptor_time_lapse_signal_completes_and_normalizes_state():
    core = FakeCore(state="run_acquisition_list")  # Core may leave a non-idle state behind
    acceptor = servers.Acceptor(core)
    acceptor.dispatch("time_lapse_start", {})  # WAIT op, milestone time_lapse
    assert operation_snapshot(core)["status"] == "processing"
    core.timelapse_active = False  # terminal/cancel signals are valid only after this
    core.sig_time_lapse_finished.emit()
    assert operation_snapshot(core)["status"] == "completed"
    assert core.state["state"] == "idle"  # _complete_time_lapse forces idle


def test_acceptor_wrong_signal_does_not_complete():
    core = FakeCore()
    acceptor = servers.Acceptor(core)
    acceptor.dispatch("time_lapse_start", {})  # milestone time_lapse
    core.sig_finished.emit()  # the finished signal is for a different op
    assert operation_snapshot(core)["status"] == "processing"


def test_acceptor_stop_disconnects_signals():
    core = FakeCore()
    acceptor = servers.Acceptor(core)
    acceptor.dispatch("start_live", {})
    acceptor.stop()  # after stop, a signal must no longer complete
    core.sig_finished.emit()
    assert operation_snapshot(core)["status"] == "processing"


# ----- the public operation view is the polling contract: it must expose stop_requested + error -----


def test_public_view_exposes_stop_requested():
    core = FakeCore()
    core._remote_session["operation"] = {
        "status": "processing",
        "command": "start_live",
        "id": "op-000001",
        "milestone": "finished",
    }
    reply = run(core, "stop", {})
    assert reply["operation"]["stop_requested"] is True


def test_public_view_exposes_error():
    core = FakeCore()
    run(core, "start_live", {})
    fail(core, MILESTONE_FINISHED, RuntimeError("motor stalled"))
    assert operation_snapshot(core)["error"] == "motor stalled"


# ----- exact Core calls: handlers must not introduce additional hardware actions -----


def test_zero_makes_only_the_zero_call():
    core = FakeCore()
    run(core, "zero", {"axes": ["x"]})
    assert core.calls == [("zero_axes", (["x"],), {})]


def test_set_zoom_forwards_exact_kwargs():
    core = FakeCore()
    run(core, "set_zoom", {"zoom": "1x"})  # zoom is slow -> wait defaults True (blocking)
    assert core.calls == [("set_zoom", ("1x",), {"wait_until_done": True, "update_etl": True})]


def test_set_laser_forwards_exact_kwargs():
    core = FakeCore()
    run(core, "set_laser", {"laser": "488 nm"})
    assert core.calls == [("set_laser", ("488 nm",), {"wait_until_done": False, "update_etl": True})]


# ----- the MCP HTTP boundary: a real loopback server exercises the token + origin + body checks -----


@contextlib.contextmanager
def _mcp_server(token="secret"):
    server = ThreadingHTTPServer(("127.0.0.1", 0), servers._make_handler(_Acc(FakeCore()), token))
    import threading

    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server.server_address[1]
    finally:
        server.shutdown()
        thread.join(timeout=5)


def _post(port, path="/mcp", headers=None, body=b""):
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        conn.request("POST", path, body=body, headers=headers or {})
        response = conn.getresponse()
        return response.status, response.read()
    finally:
        conn.close()


_GOOD = {"Origin": "http://127.0.0.1", "Authorization": "Bearer secret", "Content-Type": "application/json"}


def test_mcp_rejects_wrong_token():
    with _mcp_server() as port:
        status, _ = _post(
            port,
            headers={**_GOOD, "Authorization": "Bearer WRONG"},
            body=b'{"jsonrpc":"2.0","id":1,"method":"initialize"}',
        )
        assert status == 401


def test_mcp_rejects_disallowed_origin():
    with _mcp_server() as port:
        status, _ = _post(
            port,
            headers={**_GOOD, "Origin": "http://evil.example"},
            body=b'{"jsonrpc":"2.0","id":1,"method":"initialize"}',
        )
        assert status == 403


def test_mcp_rejects_wrong_path():
    with _mcp_server() as port:
        status, _ = _post(port, path="/not-mcp", headers=_GOOD, body=b"{}")
        assert status == 404


def test_mcp_rejects_oversized_body():
    with _mcp_server() as port:  # the cap is checked from Content-Length BEFORE
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)  # the body is read, so claim
        try:  # an oversized length and send none: clean 413
            conn.putrequest("POST", "/mcp", skip_accept_encoding=True)
            for key, value in _GOOD.items():
                conn.putheader(key, value)
            conn.putheader("Content-Length", str((1 << 20) + 1))  # over MAX_MCP_BODY_BYTES
            conn.endheaders()
            assert conn.getresponse().status == 413
        finally:
            conn.close()


def test_mcp_good_request_returns_jsonrpc_result():
    with _mcp_server() as port:
        status, raw = _post(port, headers=_GOOD, body=b'{"jsonrpc":"2.0","id":1,"method":"initialize"}')
        assert status == 200
        assert json.loads(raw)["result"]["serverInfo"]["name"] == MCP_SERVER_NAME


def test_mcp_response_ignores_windows_client_abort():
    """A timed-out client must not produce a handler traceback on Windows."""
    handler = servers._make_handler(None, "secret")

    class ClosedSocket:
        def write(self, _body):
            raise ConnectionAbortedError(10053, "client closed")

    class Response:
        wfile = ClosedSocket()

        def send_response(self, _status):
            pass

        def send_header(self, _name, _value):
            pass

        def end_headers(self):
            pass

    handler._json(Response(), 200, {"ok": True})


# ----- servers.start(): the one entry point Core calls -- fail-closed, one transport, tidy stop -----


def test_start_rejects_unknown_mode():
    with pytest.raises(ValueError):  # unknown mode is refused before any bind
        servers.start(FakeCore(), "SMTP", "127.0.0.1", 0, "secret")


def test_start_requires_a_token():
    with pytest.raises(ValueError):  # decision 3: never bind without a token
        servers.start(FakeCore(), "MCP", "127.0.0.1", 0, "")


def test_start_fails_closed_when_self_test_fails():
    import types

    blind = types.SimpleNamespace(cfg=types.SimpleNamespace())  # no stage limits -> self-test fails
    with pytest.raises(RuntimeError):  # a failed self-test means nothing was exposed
        servers.start(blind, "MCP", "127.0.0.1", 0, "secret")


def test_start_refuses_default_password_on_non_loopback():
    # The default password is public. Keep it for loopback convenience, but a non-loopback
    # bind with the unchanged default is refused before anything binds.
    from mesoSPIM.src.mesoSPIM_RemoteControl_Config import DEFAULT_TOKEN

    for host in ("0.0.0.0", "192.168.1.10", ""):
        with pytest.raises(ValueError, match="default password"):
            servers.start(FakeCore(), "MCP", host, 0, DEFAULT_TOKEN)


def test_start_allows_default_password_on_loopback():
    from mesoSPIM.src.mesoSPIM_RemoteControl_Config import DEFAULT_TOKEN

    handle = servers.start(FakeCore(), "MCP", "127.0.0.1", 0, DEFAULT_TOKEN)  # the common case still works
    handle.stop()


def test_start_mcp_binds_serves_and_stops():
    core = FakeCore()
    handle = servers.start(core, "MCP", "127.0.0.1", 0, "secret")
    try:
        assert isinstance(handle.adapter, servers.McpAdapter)
        assert not isinstance(handle.adapter, servers.TcpAdapter)  # exactly one selected transport
        assert handle.port > 0  # port 0 asked the OS for a real port
        status, raw = _post(
            handle.port, headers=_GOOD, body=b'{"jsonrpc":"2.0","id":1,"method":"initialize"}'
        )
        assert status == 200
        assert json.loads(raw)["result"]["serverInfo"]["name"] == MCP_SERVER_NAME
    finally:
        handle.stop()  # stop() tears down adapter + acceptor cleanly


def test_core_transport_helpers_replace_exactly_one_handle(monkeypatch):
    class Handle:
        port = 42123

        def __init__(self):
            self.stops = 0

        def stop(self):
            self.stops += 1

    class Signal:
        def __init__(self):
            self.events = []

        def emit(self, *event):
            self.events.append(event)

    core = FakeCore()
    old, new = Handle(), Handle()
    core._remote_control = old
    core.sig_remote_control_started = Signal()
    monkeypatch.setattr(servers, "start", lambda *_args: new)

    servers.start_for_core(core, "MCP", "127.0.0.1", 42123, "secret")
    assert old.stops == 1
    assert core._remote_control is new
    assert core.sig_remote_control_started.events == [(True, "127.0.0.1:42123")]

    servers.stop_for_core(core)
    servers.stop_for_core(core)  # idempotent; never stops twice
    assert new.stops == 1 and core._remote_control is None


def test_core_transport_helper_reports_failed_bind_without_a_handle(monkeypatch):
    class Signal:
        def __init__(self):
            self.events = []

        def emit(self, *event):
            self.events.append(event)

    core = FakeCore()
    core._remote_control = None
    core.sig_remote_control_started = Signal()

    def refuse(*_args):
        raise OSError("address already in use")

    monkeypatch.setattr(servers, "start", refuse)
    servers.start_for_core(core, "TCP", "127.0.0.1", 42000, "secret")
    assert core._remote_control is None
    assert core.sig_remote_control_started.events == [(False, "address already in use")]


def test_mcp_stop_is_prompt_and_stalled_request_cannot_actuate_afterward():
    core = FakeCore()
    handle = servers.start(core, "MCP", "127.0.0.1", 0, "secret")
    body = (
        b'{"jsonrpc":"2.0","id":1,"method":"tools/call","params":'
        b'{"name":"set_intensity","arguments":{"intensity":42}}}'
    )
    sock = socket.create_connection(("127.0.0.1", handle.port), timeout=2)
    sock.sendall(
        b"POST /mcp HTTP/1.1\r\nHost: 127.0.0.1\r\n"
        b"Origin: http://127.0.0.1\r\nAuthorization: Bearer secret\r\n"
        + f"Content-Length: {len(body)}\r\n\r\n".encode("ascii")
        + body[:10]
    )
    started = time.monotonic()
    handle.stop()
    elapsed = time.monotonic() - started
    try:
        sock.sendall(body[10:])  # if the handler survives, Acceptor is closed
    except OSError:
        pass
    finally:
        sock.close()
    assert elapsed < 2.0
    assert not any(call[0] == "set_intensity" for call in core.calls)


# ========================= Additional semantic coverage =========================

# ----- acquire_start rollback: an unschedulable start restores the list -----


def test_acquire_start_rollback_when_scheduling_raises(monkeypatch):
    from PyQt5 import QtCore

    core = FakeCore(acq_list=["ORIG"])
    original = core.state["acq_list"]

    def boom(_msec, _fn):
        raise RuntimeError("cannot schedule")

    monkeypatch.setattr(QtCore.QTimer, "singleShot", staticmethod(boom))

    with pytest.raises(RuntimeError):
        run(core, "acquire_start", {"acquisition": _acquisition()})
    assert core.state["acq_list"] is original  # exact prior object restored
    assert "prev_acq_list" not in core._remote_session
    monkeypatch.undo()
    run(core, "acquire_finish", {})  # no saved list -> a no-op
    assert core.state["acq_list"] is original


# ----- Environment limits tighten through run(), including file and malformed branches -----


def test_env_limits_tighten_through_run():
    var = commands.config.LIMITS_ENV_VAR
    os.environ[var] = '{"x": [-5, 5]}'
    try:
        core = FakeCore()
        with pytest.raises(ValidationError):
            run(core, "move_absolute", {"targets": {"x": 10}})  # 10 outside -5..5 (cfg allows ±25000)
        assert commands.effective_limits(core)["x"] == (-5.0, 5.0)
    finally:
        os.environ.pop(var, None)


def test_env_limits_from_file_path(tmp_path):
    var = commands.config.LIMITS_ENV_VAR
    path = tmp_path / "limits.json"
    path.write_text('{"y": [-3, 3]}', encoding="utf-8")
    os.environ[var] = str(path)
    try:
        core = FakeCore()
        assert commands.effective_limits(core)["y"] == (-3.0, 3.0)
        with pytest.raises(ValidationError):
            run(core, "move_absolute", {"targets": {"y": 10}})
    finally:
        os.environ.pop(var, None)


def test_env_limits_malformed_fails_closed(monkeypatch):
    # a PRESENT but malformed override must NOT be silently dropped (that would remove an operator's
    # intended tighter soft limit). Every malformed shape raises, and self_test then refuses to bind.
    var = commands.config.LIMITS_ENV_VAR
    for bad in (
        "",
        "   ",
        "{bad",
        '["not", "an", "object"]',
        '{"x": [1]}',
        '{"x": [0, 1, 2]}',
        '{"x": ["a", "b"]}',
        '{"x": [true, 1]}',
        '{"nope": [0, 1]}',
        '{"x": [5, -5]}',
        '{"x": [0, 1], "x": [0, 2]}',
    ):
        monkeypatch.setenv(var, bad)
        with pytest.raises(ValidationError):
            commands._limits_from_env()
        ok, report = commands.self_test(FakeCore())  # fail closed: nothing would bind
        assert not ok and any("could not be resolved" in line for line in report)
    monkeypatch.delenv(var, raising=False)
    assert commands._limits_from_env() == {}  # ABSENT is the only silent 'no override'


def test_configured_limits_require_finite_ordered_pairs_for_every_axis():
    core = FakeCore()
    del core.cfg.stage_parameters["x_max"]
    with pytest.raises(ValidationError, match="define both"):
        commands.effective_limits(core)

    core = FakeCore()
    core.cfg.stage_parameters["x_min"] = 1
    core.cfg.stage_parameters["x_max"] = -1
    with pytest.raises(ValidationError, match="min <= max"):
        commands.effective_limits(core)

    core = FakeCore()
    core.cfg.stage_parameters["x_min"] = True
    with pytest.raises(ValidationError, match="must be a number"):
        commands.effective_limits(core)


def test_every_axis_requires_an_effective_limit_before_binding():
    core = FakeCore()
    del core.cfg.stage_parameters["theta_min"]
    del core.cfg.stage_parameters["theta_max"]
    with pytest.raises(ValidationError, match="theta"):
        commands.effective_limits(core)
    ok, report = commands.self_test(core)
    assert ok is False and any("limits could not be resolved" in line for line in report)


# ----- Environment overrides are tighten-only intersections and can never widen limits -----


def test_env_limits_cannot_widen_the_cfg_envelope():
    var = commands.config.LIMITS_ENV_VAR
    os.environ[var] = '{"x": [-1000000000, 1000000000]}'  # try to blow the envelope wide open
    try:
        core = FakeCore()  # cfg x is [-25000, 25000]
        assert commands.effective_limits(core)["x"] == (-25000.0, 25000.0)  # clamped to cfg, not widened
        with pytest.raises(ValidationError):
            run(core, "move_absolute", {"targets": {"x": 30000}})  # still outside the real envelope
    finally:
        os.environ.pop(var, None)


def test_env_limits_non_overlapping_override_is_fail_safe():
    var = commands.config.LIMITS_ENV_VAR
    os.environ[var] = '{"x": [100000, 200000]}'  # no overlap with cfg [-25000, 25000]
    try:
        core = FakeCore()
        with pytest.raises(ValidationError, match="does not overlap"):
            commands.effective_limits(core)  # refuses the axis rather than guessing
    finally:
        os.environ.pop(var, None)


def test_env_limits_may_add_a_bound_for_an_unbounded_axis():
    var = commands.config.LIMITS_ENV_VAR
    os.environ[var] = '{"theta": [-1, 1]}'
    try:
        core = FakeCore()
        del core.cfg.stage_parameters["theta_min"]
        del core.cfg.stage_parameters["theta_max"]  # env supplies the otherwise-missing bound
        assert commands.effective_limits(core)["theta"] == (-1.0, 1.0)
        with pytest.raises(ValidationError):
            run(core, "move_absolute", {"targets": {"theta": 5}})
    finally:
        os.environ.pop(var, None)


# ----- The generic setter cannot drive blocking modes through the state key -----


def test_set_state_rejects_the_state_key():
    core = FakeCore()
    with pytest.raises(ValidationError, match="unknown state setting"):
        run(core, "set_state", {"settings": {"state": "live"}})
    assert "state" not in commands.config.SETTABLE_STATE_KEYS
    assert "state" not in run(core, "get_capabilities", {})["settable_state_keys"]


# ----- Replies never contain non-standard NaN or Infinity JSON -----


def test_jsonable_coerces_non_finite_floats():
    assert jsonable(float("nan")) == "nan"
    assert jsonable(float("inf")) == "inf"
    assert jsonable([1.0, float("-inf")]) == [1.0, "-inf"]


def test_read_with_nonfinite_state_serializes_as_valid_json():
    import json

    core = FakeCore(intensity=float("nan"))
    doc = run(core, "get_state_all", {"keys": ["intensity"]})
    json.dumps(doc, allow_nan=False)  # must not raise -> strict JSON


# ----- A non-object arguments value is invalid rather than an empty call -----


@pytest.mark.parametrize("bad", [[], "", 0, False])
def test_non_object_arguments_are_rejected(bad):
    core = FakeCore()
    with pytest.raises(ValidationError, match="must be an object"):
        run(core, "ping", bad)


def test_missing_arguments_default_to_empty():
    assert isinstance(run(FakeCore(), "ping", None), dict)  # None -> {} -> normal read, no error


# ----- A nested acquire_start cannot lose the operator's original list -----


def test_nested_acquire_start_is_refused():
    core = FakeCore(acq_list=["ORIGINAL"])
    original = core.state["acq_list"]
    run(core, "acquire_start", {"acquisition": _acquisition()})
    complete(core, MILESTONE_FINISHED)  # first reaches terminal, gate frees
    with pytest.raises(ValidationError, match="acquire_finish first"):
        run(core, "acquire_start", {"acquisition": _acquisition()})  # nested -> refused
    run(core, "acquire_finish", {})
    assert core.state["acq_list"] is original  # the ORIGINAL is restored, not lost


# ----- A closed acceptor refuses to actuate, making the Stop button a real boundary -----


def test_closed_acceptor_refuses_dispatch():
    core = FakeCore()
    acceptor = servers.Acceptor(core)
    acceptor.close()
    with pytest.raises(RuntimeError, match="shutting down"):
        acceptor.dispatch("set_intensity", {"intensity": 42})
    assert core.calls == []  # nothing reached the Core


# ----- Every settable value has an authoritative type, range, or option policy -----

_PERCENT_15 = [
    "intensity",
    "camera_delay_%",
    "camera_pulse_%",
    "etl_l_delay_%",
    "etl_l_ramp_rising_%",
    "etl_l_ramp_falling_%",
    "etl_r_delay_%",
    "etl_r_ramp_rising_%",
    "etl_r_ramp_falling_%",
    "galvo_l_duty_cycle",
    "galvo_r_duty_cycle",
    "laser_l_delay_%",
    "laser_l_pulse_%",
    "laser_r_delay_%",
    "laser_r_pulse_%",
]
_SETTER = {
    "intensity": "set_intensity",
    "camera_delay_%": "set_camera",
    "camera_pulse_%": "set_camera",
    "galvo_l_duty_cycle": "set_galvo",
    "galvo_r_duty_cycle": "set_galvo",
}


def _setter_call(key, value):
    setter = _SETTER.get(
        key, "set_etl" if key.startswith("etl_") else "set_laser_timing" if key.startswith("laser_") else None
    )
    args = {"intensity": value} if key == "intensity" else {key: value}
    return setter, args


def test_percent_key_list_matches_source():
    # anchor to the source, not just a count: if Commands adds a 16th percent-ranged key it must
    # appear here (and get enforcement below), instead of silently going unranged and unnoticed.
    assert set(_PERCENT_15) == set(commands._PERCENT_KEYS)
    assert len(_PERCENT_15) == 15


@pytest.mark.parametrize("key", _PERCENT_15)
def test_percent_range_enforced_both_entrypoints(key):
    setter, _ = _setter_call(key, 50)
    for bad in (-1, 101):
        with pytest.raises(ValidationError):
            run(FakeCore(), setter, _setter_call(key, bad)[1])
        with pytest.raises(ValidationError):
            run(FakeCore(), "set_state", {"settings": {key: bad}})
    for good in (0, 50, 100):
        run(FakeCore(), setter, _setter_call(key, good)[1])
        run(FakeCore(), "set_state", {"settings": {key: good}})


def _group_for_setting(key):
    return next((name for name, keys in commands.config.SETTING_GROUPS.items() if key in keys), None)


def test_every_settable_state_key_has_one_closed_validation_policy():
    core = FakeCore()
    classified = (
        set(commands._cfg_options(core))
        | set(commands._PERCENT_KEYS)
        | set(commands.config.PARAMETER_RANGES)
        | set(commands._BOOL_STATE_KEYS)
    )
    assert classified == set(commands.config.SETTABLE_STATE_KEYS)
    assert set(commands.config.PARAMETER_RANGES) <= set(commands.config.SETTABLE_STATE_KEYS)


def test_all_numeric_hardware_ranges_enforce_both_boundaries_and_both_entrypoints():
    for key, (low, high) in commands.config.PARAMETER_RANGES.items():
        group = _group_for_setting(key)
        for good in (low, high):
            run(FakeCore(), "set_state", {"settings": {key: good}})
            if group is not None:
                run(FakeCore(), group, {key: good})
        step = max(1.0, abs(high - low) * 0.01)
        for bad in (low - step, high + step):
            with pytest.raises(ValidationError, match="allowed range"):
                run(FakeCore(), "set_state", {"settings": {key: bad}})
            if group is not None:
                with pytest.raises(ValidationError, match="allowed range"):
                    run(FakeCore(), group, {key: bad})


def test_all_configured_options_accept_members_and_reject_nonmembers():
    dedicated = {
        "filter": "set_filter",
        "zoom": "set_zoom",
        "laser": "set_laser",
        "shutterconfig": "set_shutterconfig",
    }
    core = FakeCore()
    for key, options in commands._cfg_options(core).items():
        assert options, f"fake configuration must exercise at least one {key} option"
        setter = dedicated.get(key, _group_for_setting(key))
        for value in options:
            run(FakeCore(), "set_state", {"settings": {key: value}})
            run(FakeCore(), setter, {key: value})
        bad = 999 if key in commands._NUMERIC_OPTION_KEYS else "__not_configured__"
        with pytest.raises(ValidationError, match="is not one of"):
            run(FakeCore(), "set_state", {"settings": {key: bad}})
        with pytest.raises(ValidationError, match="is not one of"):
            run(FakeCore(), setter, {key: bad})


@pytest.mark.parametrize("good", [False, True])
def test_boolean_hardware_setting_is_strict(good):
    run(FakeCore(), "set_state", {"settings": {"galvo_amp_scale_w_zoom": good}})
    run(FakeCore(), "set_galvo", {"galvo_amp_scale_w_zoom": good})


@pytest.mark.parametrize("bad", [0, 1, "true", None])
def test_boolean_hardware_setting_rejects_coercion(bad):
    with pytest.raises(ValidationError, match="boolean"):
        run(FakeCore(), "set_galvo", {"galvo_amp_scale_w_zoom": bad})


def test_hardware_parameters_are_bounded_to_gui_ranges():
    # Voltages, frequency, phase, and timings use the upstream GUI spin-box ranges.
    # an implausible value like etl_l_amplitude=999999 is now REFUSED before Core, not accepted.
    cases = {  # setter, key, an out-of-range value, and an in-range value
        "set_etl": ("etl_l_amplitude", 999999.0, 1.5),  # 0..2 V
        "set_galvo": ("galvo_l_frequency", 1000.0, 200.0),  # 0..400 Hz
        "set_camera": ("camera_exposure_time", 0.0, 0.02),  # 0.001..5 seconds
    }
    for setter, (key, bad, good) in cases.items():
        with pytest.raises(ValidationError, match="allowed range"):
            run(FakeCore(), setter, {key: bad})
        with pytest.raises(ValidationError, match="allowed range"):
            run(FakeCore(), "set_state", {"settings": {key: bad}})  # same bound via the generic setter
        run(FakeCore(), setter, {key: good})  # an in-range value passes
    with pytest.raises(ValidationError):
        run(FakeCore(), "set_etl", {"etl_l_amplitude": "loud"})  # and a non-number is still refused


def test_get_limits_reports_hardware_parameter_ranges():
    doc = run(FakeCore(), "get_limits", {})
    assert doc["enforced"]["parameters"]["etl_l_amplitude"]["range"] == [0.0, 2.0]
    assert doc["enforced"]["parameters"]["galvo_l_frequency"]["range"] == [0.0, 400.0]
    assert doc["enforced"]["parameters"]["camera_exposure_time"]["range"] == [0.001, 5.0]


def test_acquisition_etl_field_is_bounded():
    with pytest.raises(ValidationError, match="allowed range"):
        run(FakeCore(), "acquire_start", {"acquisition": _acquisition(etl_l_amplitude=999999.0)})


@pytest.mark.parametrize("bad", [-1, 0, 2.9, "2048", True])
def test_camera_dimensions_must_be_positive_integers(bad):
    core = FakeCore()
    core.cfg.camera_parameters["x_pixels"] = bad
    with pytest.raises(ValidationError, match="positive integers"):
        run(core, "get_config", {})


@pytest.mark.parametrize("bad", [0.9, "1", True, -1])
def test_time_lapse_interval_is_a_nonnegative_integer(bad):
    with pytest.raises(ValidationError, match="interval_sec"):
        run(FakeCore(), "time_lapse_start", {"interval_sec": bad})


def test_acquisition_plane_metadata_round_trips_when_geometry_disagrees():
    core = FakeCore()
    row = {"z_start": 0, "z_end": 4, "z_step": 1, "planes": 4}
    reply = run(core, "set_acquisition_list", {"acquisitions": [row], "selected_row": 0})
    assert reply["operation"]["result"]["count"] == 1
    assert core.state["acq_list"][0]["planes"] == 4
    assert core.state["acq_list"][0].get_image_count() == 5


@pytest.mark.parametrize("bad", [1.0, "1", True, 0, -1])
def test_acquisition_plane_count_requires_a_json_integer(bad):
    with pytest.raises(ValidationError, match="positive integer"):
        run(
            FakeCore(),
            "acquire_start",
            {"acquisition": {"z_start": 0, "z_end": 0, "z_step": 1, "planes": bad}},
        )


def test_acquisition_plane_metadata_has_a_hard_ceiling():
    with pytest.raises(ValidationError, match="metadata maximum"):
        run(
            FakeCore(),
            "acquire_start",
            {"acquisition": {"z_start": 0, "z_end": 0, "z_step": 1, "planes": MAX_ACQUISITION_PLANES + 1}},
        )


def test_extremely_small_z_step_is_a_validation_error_not_an_overflow():
    with pytest.raises(ValidationError, match="maximum"):
        run(FakeCore(), "acquire_start", {"acquisition": {"z_start": 0, "z_end": 1, "z_step": 1e-320}})


def test_acquisition_geometry_has_a_hard_plane_ceiling():
    with pytest.raises(ValidationError, match="maximum"):
        run(FakeCore(), "acquire_start", {"acquisition": {"z_start": -25000, "z_end": 25000, "z_step": 0.01}})


# ----- Read-document shapes -----


def test_get_progress_stable_null_state():
    from fakes import FakeState

    core = FakeCore()
    core.state = FakeState(state="idle")  # omit all four counter keys
    doc = run(core, "get_progress", {})
    assert doc["current_plane"] is None and doc["total_planes"] is None
    assert doc["current_acquisition"] is None and doc["total_acquisitions"] is None


def test_get_state_all_no_keys_returns_whole_state():
    core = FakeCore()
    doc = run(core, "get_state_all", {})
    assert len(doc) == len(core.state)


def test_get_state_seeded_readback():
    doc = run(FakeCore(), "get_state", {})
    assert doc["state"] == "idle"
    assert set(doc["position"]) == {"x", "y", "z", "f", "theta"}
    assert doc["intensity"] == 10


def test_get_info_full_shape():
    doc = run(FakeCore(), "get_info", {})
    for key in ("stage_type", "protocol", "etl_config_path", "last_acquisition_path", "operation"):
        assert key in doc, key
    assert doc["stage_type"] == "DemoStage"


def test_get_limits_all_four_sections():
    doc = run(FakeCore(), "get_limits", {})
    for key in ("stage", "camera", "startup", "enforced"):
        assert key in doc, key
    assert doc["camera"] == {"x_pixels": 2048, "y_pixels": 2048, "subsampling": [1, 2, 4]}


# ----- stale-signal completion is guarded by the operation/state association -----
# An acquisition WAIT op is completed by its milestone signal ONLY once the core has left its running
# state ('run_acquisition_list' / 'run_selected_acquisition'). mesoSPIM_Core clears state->'idle'
# before emitting sig_finished for acquisitions, so a stray same-milestone signal arriving while the
# acquisition is still running is ignored — it cannot complete a still-running acquisition early.
# The same association protects live/visual/alignment modes: Core.stop() changes state to idle before
# their loop emits sig_finished, so only that post-stop signal may complete the operation.


def test_stray_finished_ignored_while_acquisition_still_running():
    core = FakeCore()
    run(core, "run_acquisition_list", {})
    core.state["state"] = "run_acquisition_list"  # model start() before its sync return
    complete(core, MILESTONE_FINISHED)  # a stray finished while still running
    assert operation_snapshot(core)["status"] == "processing"  # ignored -> op protected
    core.state["state"] = "idle"  # the acquisition actually ends
    complete(core, MILESTONE_FINISHED)  # the real completion lands
    assert operation_snapshot(core)["status"] == "completed"


def test_late_signal_does_not_complete_a_newer_running_acquisition():
    # op1 completes; op2 starts and is genuinely running; a late op1 signal must not
    # complete op2 while op2's acquisition is still active. The command sets the run state itself.
    core = FakeCore()
    run(core, "run_acquisition_list", {})
    core.state["state"] = "idle"  # op1's acquisition ended
    complete(core, MILESTONE_FINISHED)
    assert operation_snapshot(core)["status"] == "completed"  # op1 completes normally
    run(core, "run_acquisition_list", {})  # op2 starts
    core.state["state"] = "run_acquisition_list"  # model op2 inside Core.start()
    complete(core, MILESTONE_FINISHED)  # a late/stray finished
    assert operation_snapshot(core)["status"] == "processing"  # op2 NOT completed early
    core.state["state"] = "idle"
    complete(core, MILESTONE_FINISHED)  # op2's real end
    assert operation_snapshot(core)["status"] == "completed"


def test_mode_completion_ignores_stale_signal_while_mode_is_running():
    core = FakeCore()
    run(core, "start_live", {})
    complete(core, MILESTONE_FINISHED)  # stale signal while state=live
    assert operation_snapshot(core)["status"] == "processing"
    core.state["state"] = "idle"  # Core.stop runs before real signal
    complete(core, MILESTONE_FINISHED)
    assert operation_snapshot(core)["status"] == "completed"


def test_matching_signal_cannot_cancel_a_not_yet_started_callback():
    core = FakeCore()
    with defer_recorder() as pending:
        run(core, "start_live", {})
        complete(core, MILESTONE_FINISHED)  # stale from a prior operation
        assert operation_snapshot(core)["status"] == "processing"
        assert core.calls == []
        pending.pop(0)()
        pending.pop(0)()
    assert core.calls[0][0] == "set_state"
    core.state["state"] = "idle"
    complete(core, MILESTONE_FINISHED)
    assert operation_snapshot(core)["status"] == "completed"


# ----- Guarded recovery clears a wedged operation only when Core is independently idle -----


def test_clear_stuck_operation_recovers_only_when_core_is_idle():
    core = FakeCore()
    run(core, "run_acquisition_list", {})  # op holds the gate
    core.state["state"] = "idle"  # hardware finished but the signal was lost
    reply = run(core, "clear_stuck_operation", {})  # EMERGENCY: runs while the gate is held
    assert reply["cleared"] is True and reply["operation"]["status"] == "idle"
    run(core, "set_intensity", {"intensity": 20})  # gate is free again -> a mutation works


def test_clear_stuck_operation_refuses_while_core_still_running():
    core = FakeCore()
    run(core, "run_acquisition_list", {})  # op holds the gate
    core.state["state"] = "run_acquisition_list"  # hardware is still running
    reply = run(core, "clear_stuck_operation", {})  # core NOT idle -> refuse (op is running)
    assert reply["cleared"] is False
    assert operation_snapshot(core)["status"] == "processing"  # the running op is untouched
    with pytest.raises(BusyError):
        run(core, "set_intensity", {"intensity": 20})  # still busy -> nothing was aborted


def test_clear_stuck_operation_noop_when_idle():
    core = FakeCore()
    reply = run(core, "clear_stuck_operation", {})  # nothing holding the gate
    assert reply["cleared"] is False and core.calls == []


def test_clear_cannot_release_a_deferred_action_before_it_starts():
    core = FakeCore()
    with defer_recorder() as pending:
        reply = run(core, "start_live", {})
        assert reply["operation"]["status"] == "processing"
        recovery = run(core, "clear_stuck_operation", {})
        assert recovery["cleared"] is False
        assert "has not started" in recovery["reason"]

        # The operator's explicit stop cancels a still-queued action. When the Qt callback later
        # runs, its operation-id/phase claim fails and no hardware call is made.
        run(core, "stop_activity", {})
        pending[0]()

    assert not any(call[0] == "set_state" for call in core.calls)
    assert operation_snapshot(core) == {
        "id": "op-000001",
        "command": "start_live",
        "status": "completed",
        "stop_requested": True,
    }


def test_clear_cannot_claim_an_unconfirmed_stage_target_is_finished():
    core = FakeCore()
    with defer_recorder() as pending:
        run(core, "move_absolute", {"targets": {"x": 100}})
        pending.pop(0)()  # enter movement handler
        pending.pop(0)()  # command issued; readback poll still pending
        recovery = run(core, "clear_stuck_operation", {})
        assert recovery["cleared"] is False
        assert "target has not been confirmed" in recovery["reason"]
        pending.pop(0)()
    assert operation_snapshot(core)["status"] == "completed"


def test_time_lapse_cannot_be_recovered_while_active_between_timepoints():
    core = FakeCore()
    run(core, "time_lapse_start", {"timepoints": 2, "interval_sec": 1})
    assert core.state["state"] == "idle" and core.timelapse_active is True
    reply = run(core, "clear_stuck_operation", {})
    assert reply["cleared"] is False
    assert "time lapse is still active" in reply["reason"]

    # Once an explicit stop has made the independent time-lapse flag false, a missing terminal
    # signal may be recovered and the remote gate becomes usable again.
    run(core, "time_lapse_stop", {})
    assert run(core, "clear_stuck_operation", {})["cleared"] is True
    run(core, "set_intensity", {"intensity": 20})


def test_acquisition_preflight_refusal_fails_instead_of_wedging():
    core = FakeCore()
    acceptor = servers.Acceptor(core)

    def reject_during_preflight(row=None):
        # This mirrors upstream's refusal branches: emit sig_finished without first leaving the
        # acquisition run state, then return. The signal is ignored as premature; the wrapper
        # reconciles the synchronous return into a failed, terminal operation.
        core._record("start", row=row)
        core.sig_finished.emit()

    core.start = reject_during_preflight
    reply = acceptor.dispatch("run_acquisition_list", {})
    assert reply["operation"]["status"] == "failed"
    assert "preflight" in reply["operation"]["error"]
    assert core.state["state"] == "idle"
    acceptor.dispatch("set_intensity", {"intensity": 20})


# ----- Cancelled calls are dropped before execution -----


def test_cancelled_call_is_dropped_without_running():
    core = FakeCore()
    acceptor = servers.Acceptor(core)
    call = servers._Call("get_position", {})
    call.cancelled = True
    acceptor._execute(call)
    assert call.result is None
    assert core.calls == []


# ----- Values exactly on accepted boundaries -----


def test_axis_boundaries_are_inclusive():
    core = FakeCore()
    for axis, (low, high) in commands.effective_limits(core).items():
        run(core, "move_absolute", {"targets": {axis: low}})  # exact min accepted
        run(core, "move_absolute", {"targets": {axis: high}})  # exact max accepted


def test_percent_boundaries_accepted():
    for value in (0, 50, 100):
        run(FakeCore(), "set_intensity", {"intensity": value})


# ----- A limit violation reports the valid range so a client can correct its request -----


def test_absolute_limit_violation_reports_the_allowed_range():
    core = FakeCore()  # cfg x is [-25000, 25000]
    with pytest.raises(ValidationError) as exc:
        run(core, "move_absolute", {"targets": {"x": 999999}})
    assert "allowed range [-25000.0, 25000.0]" in str(exc.value)


def test_relative_limit_violation_reports_the_allowed_range():
    core = FakeCore(position={"x_pos": 24999.0})
    with pytest.raises(ValidationError) as exc:
        run(core, "move_relative", {"deltas": {"x": 10}})  # 24999 + 10 -> 25009, past 25000
    message = str(exc.value)
    assert "allowed range [-25000.0, 25000.0]" in message
    assert "25009" in message  # the value it would have reached


def test_percent_limit_violation_reports_the_allowed_range():
    with pytest.raises(ValidationError) as exc:
        run(FakeCore(), "set_intensity", {"intensity": 250})
    assert "allowed range [0, 100]" in str(exc.value)


# ----- F3: every stage-driving path goes through the effective-limit preflight -----


def test_preset_move_outside_limits_is_refused(monkeypatch):
    # a configured preset drives the stage, so it is bounded like any move: a tightened soft limit
    # that excludes the load position refuses load_sample BEFORE the gate (nothing reaches the Core).
    monkeypatch.setenv(commands.config.LIMITS_ENV_VAR, '{"y": [-5, 5]}')
    core = FakeCore()  # y_load_position is 45000, outside [-5, 5]
    with pytest.raises(ValidationError, match="allowed range"):
        run(core, "load_sample", {})
    assert core.calls == []  # refused pre-gate; the stage never moved
    assert core._remote_session["counter"] == 0


def test_preset_within_limits_still_moves():
    core = FakeCore()  # no override -> 45000 within cfg [-50000, 50000]
    run(core, "load_sample", {})
    assert core.calls == [
        ("move_absolute", ({"y_abs": 45000.0},), {"wait_until_done": False, "use_internal_position": False})
    ]


@pytest.mark.parametrize("value", (True, float("nan"), "not-a-position"))
def test_preset_refuses_invalid_configured_position_before_opening_gate(value):
    core = FakeCore()
    core.cfg.stage_parameters["y_load_position"] = value
    with pytest.raises(ValidationError, match="stage_parameters.y_load_position"):
        run(core, "load_sample", {})
    assert core.calls == []
    assert core._remote_session["counter"] == 0


def test_installed_list_revalidated_against_a_tightened_limit(monkeypatch):
    # install a list that is in-limit now, THEN tighten the soft limit, THEN run: the run must
    # re-check the installed rows and refuse, even though the install-time check passed.
    core = FakeCore()
    run(core, "set_acquisition_list", {"acquisitions": [_acquisition(x_pos=10000)]})
    monkeypatch.setenv(commands.config.LIMITS_ENV_VAR, '{"x": [-5, 5]}')
    for name in ("run_acquisition_list", "run_selected_acquisition", "time_lapse_start"):
        with pytest.raises(ValidationError, match="installed acquisitions"):
            run(core, name, {})


def test_gui_installed_list_is_revalidated_too():
    # a list installed OUTSIDE our accept (as the GUI would) is still re-checked when a remote run
    # starts it, so an out-of-range row cannot drive the stage.
    from mesoSPIM.src.mesoSPIM_RemoteControl_Commands import _make_acquisition_list

    core = FakeCore()
    core.state["acq_list"] = _make_acquisition_list([{"x_pos": 999999, "planes": 1}])
    with pytest.raises(ValidationError, match="installed acquisitions"):
        run(core, "run_acquisition_list", {})
    assert core.calls == []  # never started


def test_installed_nonnumeric_axis_field_is_refused():
    # a present-but-nonnumeric stage target is malformed and must be refused (fail closed), not skipped
    from mesoSPIM.src.mesoSPIM_RemoteControl_Commands import _make_acquisition_list

    core = FakeCore()
    core.state["acq_list"] = _make_acquisition_list([{"x_pos": "not-a-number", "planes": 1}])
    with pytest.raises(ValidationError):
        run(core, "run_acquisition_list", {})
    assert core.calls == []


def test_bad_input_shape_reports_what_was_expected():
    # a wrong-shape input (not just a wrong command name) is a `validation` error, and the message
    # names the expected shape so a client can correct its request, just like a reported range.
    core = FakeCore()
    with pytest.raises(ValidationError, match=r"valid axes are \['x', 'y', 'z', 'f', 'theta'\]"):
        run(core, "move_absolute", {"targets": {"nope": 1}})
    with pytest.raises(ValidationError, match="must be a non-empty object of axis"):
        run(core, "move_absolute", {"targets": 5})  # wrong type entirely
    with pytest.raises(ValidationError, match="is not one of"):
        run(core, "set_filter", {"filter": "NOPE"})  # lists the configured options


# ----- Remaining validation and execution branches -----


def test_reload_etl_wait_false_uses_non_blocking_signal():
    core = FakeCore()
    run(core, "reload_etl_config", {"path": "etl.csv", "wait": False})
    names = [c[0] for c in core.calls]
    assert "emit:sig_state_request" in names
    assert "emit:sig_state_request_and_wait_until_done" not in names


def test_stat_files_missing_and_sizes(tmp_path):
    present = tmp_path / "a.txt"
    present.write_text("hi", encoding="utf-8")
    absent = str(tmp_path / "nope.txt")
    out = run(FakeCore(), "stat_files", {"files": [str(present), absent]})
    assert out["missing"] == [absent]
    assert out["sizes"][str(present)] == 2


def test_acquire_start_response_keys():
    reply = run(FakeCore(), "acquire_start", {"acquisition": _acquisition(2)})
    result = reply["operation"]["result"]
    keys = set(result)
    assert keys == {"started", "scheduled", "files", "planes", "pixels"}
    assert result["planes"] == 2 and result["pixels"] == [2048, 2048]


# ----- Disk-space and motion-limit reads remain available while busy -----


def test_reads_served_while_wait_holds_gate():
    core = FakeCore()
    run(core, "start_live", {})  # WAIT op holds the gate
    assert operation_snapshot(core)["status"] == "processing"
    assert "free_bytes" in run(core, "get_disk_space", {})
    assert "outside_limits" in run(core, "check_motion_limits", {})


# ----- TCP connection and ready-read draining stays non-recursive -----


def test_qt_adapter_drains_prebuffered_auth_and_nonrecursive_command():
    """A fast client sends the command the instant it sees 'OK', while the non-recursive readyRead
    drain is still running; the adapter must answer it from the same loop, not lose it."""
    core = FakeCore()

    class _Sig:
        def connect(self, slot, *a, **k):
            self.slot = slot

    class _Conn:
        def __init__(self):
            self.readyRead = _Sig()
            self.disconnected = _Sig()
            self.incoming = bytearray(servers.frame("secret"))
            self.writes = []

        def bytesAvailable(self):
            return len(self.incoming)

        def readAll(self):
            data = bytes(self.incoming)
            self.incoming.clear()
            return data

        def write(self, data):
            data = bytes(data)
            self.writes.append(data)
            if data == servers.frame("OK"):
                self.incoming.extend(servers.frame('{"ping": {}}'))

        def flush(self):
            pass

        def disconnectFromHost(self):
            pass

        def deleteLater(self):
            pass

    class _Pending:
        def __init__(self, conn):
            self.conn = conn

        def hasPendingConnections(self):
            return self.conn is not None

        def nextPendingConnection(self):
            conn, self.conn = self.conn, None
            return conn

    conn = _Conn()
    adapter = servers.TcpAdapter()
    adapter._acceptor = _Acc(core)
    adapter._token = "secret"
    adapter._clients = {}
    adapter._server = _Pending(conn)

    adapter._on_new_connection()

    assert conn.writes[0] == servers.frame("OK")
    header, payload = conn.writes[1].split(b"\n", 1)
    assert int(header) == len(payload)
    assert payload.startswith(OK_MARKER.encode("ascii"))
    assert json.loads(payload[len(OK_MARKER) :])["pong"] is True


def test_qt_adapter_ignores_callbacks_for_deleted_sockets():
    """A queued readyRead or write after Qt deletes its socket must be harmless."""

    class _DeletedConn(_Conn):
        def bytesAvailable(self):
            raise RuntimeError("wrapped C/C++ object of type QTcpSocket has been deleted")

        def write(self, _data):
            raise RuntimeError("wrapped C/C++ object of type QTcpSocket has been deleted")

        def disconnectFromHost(self):
            raise RuntimeError("wrapped C/C++ object of type QTcpSocket has been deleted")

    adapter = _make_tcp(FakeCore(), "secret")
    stale_read = _DeletedConn()
    adapter._clients[stale_read] = {"decoder": servers.FrameDecoder(), "authed": False}
    adapter._on_ready(stale_read)
    assert stale_read not in adapter._clients

    stale_write = _DeletedConn()
    adapter._clients[stale_write] = {"decoder": servers.FrameDecoder(), "authed": True}
    adapter._send(stale_write, "OK")
    assert stale_write not in adapter._clients
