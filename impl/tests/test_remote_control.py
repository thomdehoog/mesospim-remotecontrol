"""Offline unit tests for the Remote Control redesign. Qt-free (fake PyQt5 from conftest)."""
import json

import pytest

from mesoSPIM.src.mesoSPIM_RemoteControl_Dispatcher import (
    run, complete, operation_snapshot, jsonable, error_info,
    strict_json_loads, parse_call, COMMANDS, ValidationError, BusyError,
    UnknownCommand, READ, ACTION, WAIT, EMERGENCY)
from mesoSPIM.src import mesoSPIM_RemoteControl_Commands as commands
from mesoSPIM.src import mesoSPIM_RemoteControl_Servers as servers
from fakes import FakeCore, FakeCfg


def _acquisition(planes=1, **overrides):
    row = {"z_start": 0, "z_end": planes - 1, "z_step": 1, "planes": planes}
    row.update(overrides)
    return row


# ----- registry / vocabulary -----

def test_command_count_is_53():
    assert len(COMMANDS) == 53
    assert "set_mode" not in COMMANDS
    assert "snap" not in COMMANDS                  # the remote snapshot feature was removed
    assert "get_snap_image" not in COMMANDS
    assert "execute_stage_program" not in COMMANDS  # dropped: an opaque stage program can't be bounded
    assert "clear_stuck_operation" in COMMANDS     # guarded recovery for a never-signalled WAIT
    assert "get_manual" in COMMANDS               # the client guide, exposed over both transports

def test_every_command_has_a_kind():
    for cmd in COMMANDS.values():
        assert cmd.kind in (READ, ACTION, WAIT, EMERGENCY)

def test_disk_and_motion_are_read():
    assert COMMANDS["get_disk_space"].kind == READ
    assert COMMANDS["check_motion_limits"].kind == READ


# ----- primitives -----

def test_integer_rejects_bool_and_enforces_range():
    assert commands.integer({"n": 3}, "n", minimum=1) == 3
    with pytest.raises(ValidationError):
        commands.integer({"n": True}, "n")
    with pytest.raises(ValidationError):
        commands.integer({"n": 0}, "n", minimum=1)
    with pytest.raises(ValidationError):
        commands.integer({"n": "3"}, "n")

def test_number_bounds_and_finite():
    assert commands.number({"v": 50}, "v", (0, 100)) == 50.0
    with pytest.raises(ValidationError):
        commands.number({"v": 101}, "v", (0, 100))
    with pytest.raises(ValidationError):
        commands.number({"v": 10 ** 400}, "v")     # overflow -> not finite -> rejected

def test_axis_map_and_axes_list():
    assert commands.axis_map({"t": {"x": 1}}, "t") == {"x": 1.0}
    with pytest.raises(ValidationError):
        commands.axis_map({"t": {"q": 1}}, "t")
    assert commands.axes_list({}, "axes") == list(commands.config.AXES)
    assert commands.axes_list({"axes": []}, "axes") == list(commands.config.AXES)
    assert commands.axes_list({"axes": ["x"]}, "axes") == ["x"]


# ----- dispatcher: gate, three-error model, and reply shape -----

def test_read_bypasses_gate_and_is_unwrapped():
    core = FakeCore()
    core._remote_session["operation"] = {"status": "processing", "command": "start_live",
                                         "id": "op-000001", "milestone": "finished"}
    out = run(core, "get_position", {})            # READ works while busy
    assert "operation" not in out and "accepted" not in out
    assert set(out) == {"x", "y", "z", "f", "theta"}

def test_action_refused_while_busy_leaves_running_op_untouched():
    core = FakeCore()
    core._remote_session["operation"] = {"status": "processing", "command": "start_live",
                                         "id": "op-000001", "milestone": "finished"}
    before = dict(core._remote_session["operation"])
    with pytest.raises(BusyError):
        run(core, "move_absolute", {"targets": {"x": 10}})
    assert core._remote_session["operation"] == before
    assert core._remote_session["counter"] == 0    # no op minted

def test_stage_move_is_nonblocking_and_wraps_reply():
    core = FakeCore()
    reply = run(core, "move_absolute", {"targets": {"x": 100}})
    assert reply["accepted"] is True
    assert reply["accepted_command"] == "move_absolute"
    assert reply["operation"]["status"] == "completed"
    assert reply["target"] == {"x": 100.0}
    assert reply["operation"]["target"] == {"x": 100.0}
    assert ("move_absolute", ({"x_abs": 100.0},), {"wait_until_done": False}) == core.calls[0]

def test_validation_error_opens_no_operation():
    core = FakeCore()
    with pytest.raises(ValidationError):
        run(core, "move_absolute", {"targets": {"x": 999999}})   # out of range
    assert core._remote_session["counter"] == 0
    assert core.calls == []

def test_execute_failure_marks_operation_failed():
    core = FakeCore()
    def boom(c, a):
        raise RuntimeError("hardware said no")
    # swap the registered execute for one that raises after the gate opens
    original = COMMANDS["move_absolute"]
    COMMANDS["move_absolute"] = original.__class__(
        original.name, original.kind, boom, original.hint, original.accept, original.milestone)
    try:
        with pytest.raises(RuntimeError):
            run(core, "move_absolute", {"targets": {"x": 10}})
    finally:
        COMMANDS["move_absolute"] = original
    assert core._remote_session["operation"]["status"] == "failed"
    assert "hardware said no" in core._remote_session["operation"]["error"]

def test_emergency_marks_running_op_stopping():
    core = FakeCore()
    core._remote_session["operation"] = {"status": "processing", "command": "start_live",
                                         "id": "op-000001", "milestone": "finished"}
    reply = run(core, "stop", {})
    assert core._remote_session["operation"]["status"] == "stopping"
    assert core._remote_session["operation"]["stop_requested"] is True
    assert reply["operation"]["status"] == "stopping"

def test_close_shutters_does_not_stop_running_op():
    core = FakeCore()
    core._remote_session["operation"] = {"status": "processing", "command": "run_acquisition_list",
                                         "id": "op-000001", "milestone": "finished"}
    run(core, "close_shutters", {})
    assert core._remote_session["operation"]["status"] == "processing"   # untouched


# ----- jsonable / strict json / error_info -----

def test_jsonable_recurses_and_stringifies():
    assert jsonable({"a": (1, 2), "b": {3: "x"}}) == {"a": [1, 2], "b": {"3": "x"}}
    class Weird:
        pass
    assert isinstance(jsonable(Weird()), str)

def test_strict_json_rejects_duplicates_and_nonfinite():
    assert parse_call('{"ping": {}}') == ("ping", {})
    assert parse_call('{"ping": null}') == ("ping", {})
    with pytest.raises(ValidationError):
        strict_json_loads('{"a": 1, "a": 2}')
    with pytest.raises(ValidationError):
        strict_json_loads('{"a": NaN}')
    with pytest.raises(ValidationError):
        parse_call('{"a": {}, "b": {}}')           # two keys

def test_error_info_codes():
    assert error_info(ValidationError("x"))[0] == "validation"
    assert error_info(BusyError("x"))[0] == "busy"
    assert error_info(UnknownCommand("x"))[0] == "unknown_command"
    assert error_info(KeyError("x"))[0] == "execution"    # a plain KeyError is NOT unknown_command
    assert error_info(RuntimeError("x"))[0] == "execution"


# ----- validation of representative commands -----

def test_set_filter_option_and_unknown_arg():
    core = FakeCore()
    run(core, "set_filter", {"filter": "Empty"})
    with pytest.raises(ValidationError):
        run(core, "set_filter", {"filter": "NOPE"})
    with pytest.raises(ValidationError):
        run(core, "set_filter", {"filter": "Empty", "waite": True})   # only() catches the typo

def test_set_intensity_range():
    core = FakeCore()
    run(core, "set_intensity", {"intensity": 50})
    with pytest.raises(ValidationError):
        run(core, "set_intensity", {"intensity": 250})

def test_set_state_rejects_state_and_unknown_key():
    core = FakeCore()
    with pytest.raises(ValidationError):
        run(core, "set_state", {"settings": {"state": "live"}})   # 'state' is not settable
    with pytest.raises(ValidationError):
        run(core, "set_state", {"settings": {"x_max": 1}})

def test_acquisition_field_validation():
    core = FakeCore()
    good = {"acquisition": _acquisition(3, x_pos=0, intensity=50, filter="Empty")}
    run(core, "acquire_start", good)
    with pytest.raises(ValidationError):
        run(core, "acquire_start", {"acquisition": {"intensity": 101}})   # out of range
    with pytest.raises(ValidationError):
        run(core, "acquire_start", {"acquisition": {"filter": "NOPE"}})   # bad enum
    with pytest.raises(ValidationError):
        run(core, "acquire_start", {"acquisition": {"planes": 0}})

def test_move_relative_uses_current_plus_delta_and_serial_fallback():
    core = FakeCore(position={"x_pos": 24999.0})
    with pytest.raises(ValidationError):
        run(core, "move_relative", {"deltas": {"x": 10}})               # 24999+10 > 25000
    core2 = FakeCore(position={"x_pos": 0.0})
    reply = run(core2, "move_relative", {"deltas": {"x": 10}})
    assert reply["target"] == {"x": 10.0}
    assert reply["operation"]["target"] == {"x": 10.0}
    assert core2.calls[0][0] == "move_relative"                          # fell back to core
    assert core2.calls[0][2] == {"wait_until_done": False}


@pytest.mark.parametrize("current", (None, True, float("nan"), float("inf")))
def test_move_relative_rejects_invalid_position_readback_before_opening_gate(current):
    core = FakeCore(position={"x_pos": current})
    with pytest.raises(ValidationError, match="current x position"):
        run(core, "move_relative", {"deltas": {"x": 1}})
    assert core.calls == []
    assert core._remote_session["counter"] == 0


def test_etl_missing_source_is_pre_gate_validation():
    core = FakeCore(ETL_cfg_file="")                # no state fallback and no path arg
    with pytest.raises(ValidationError):
        run(core, "reload_etl_config", {})
    assert core._remote_session["counter"] == 0     # rejected BEFORE the gate opened


# ----- WAIT completion via the immediate-singleShot shim -----

def test_start_live_stays_processing_until_signal():
    core = FakeCore()
    reply = run(core, "start_live", {})
    assert reply["operation"]["status"] == "processing"
    assert ("set_state", ("live",), {}) == core.calls[0]
    core.state["state"] = "idle"
    complete(core, commands.config.MILESTONE_FINISHED)   # simulate sig_finished
    assert operation_snapshot(core)["status"] == "completed"


# ----- self_test -----

def test_self_test_passes_with_real_cfg():
    core = FakeCore()
    ok, report = commands.self_test(core)
    assert ok is True
    assert any("in-range" in line for line in report)

def test_self_test_fails_without_limits():
    class NoLimitsCfg(FakeCfg):
        stage_parameters = {}
    class NoLimitsCore:
        cfg = NoLimitsCfg()
    ok, report = commands.self_test(NoLimitsCore())
    assert ok is False
    assert any("limits could not be resolved" in line for line in report)


# ----- MCP reply layer -----

def test_mcp_initialize_and_tools_list():
    core = FakeCore()
    class Acc:
        def dispatch(self, name, args):
            return run(core, name, args)
    init = servers._mcp_reply(Acc(), {"jsonrpc": "2.0", "id": 1, "method": "initialize"})
    assert init["result"]["serverInfo"]["name"]
    assert "get_manual" in init["result"]["instructions"]   # points the client at the manual
    tools = servers._mcp_reply(Acc(), {"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
    assert len(tools["result"]["tools"]) == 53

def test_mcp_tools_call_success_and_error():
    core = FakeCore()
    class Acc:
        def dispatch(self, name, args):
            return run(core, name, args)
    ok = servers._mcp_reply(Acc(), {"jsonrpc": "2.0", "id": 3, "method": "tools/call",
                                    "params": {"name": "get_position", "arguments": {}}})
    assert ok["result"]["isError"] is False
    bad = servers._mcp_reply(Acc(), {"jsonrpc": "2.0", "id": 4, "method": "tools/call",
                                     "params": {"name": "move_absolute",
                                                "arguments": {"targets": {"x": 999999}}}})
    assert bad["result"]["isError"] is True
    payload = json.loads(bad["result"]["content"][0]["text"])
    assert payload["error"]["code"] == "validation"

def test_mcp_notification_and_unknown_method():
    assert servers._mcp_reply(
        None, {"jsonrpc": "2.0", "method": "tools/list"}) is None     # id-less notification
    unknown = servers._mcp_reply(None, {"jsonrpc": "2.0", "id": 9, "method": "nope"})
    assert unknown["error"]["code"] == -32601


@pytest.mark.parametrize("message,code", [
    ([], -32600),
    ({"id": 1, "method": "tools/list"}, -32600),
    ({"jsonrpc": "1.0", "id": 1, "method": "tools/list"}, -32600),
    ({"jsonrpc": "2.0", "id": None, "method": "tools/list"}, -32600),
    ({"jsonrpc": "2.0", "id": True, "method": "tools/list"}, -32600),
    ({"jsonrpc": "2.0", "id": 1, "method": None}, -32600),
    ({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": []}, -32602),
])
def test_mcp_validates_jsonrpc_envelope(message, code):
    reply = servers._mcp_reply(None, message)
    assert reply["jsonrpc"] == "2.0"
    assert reply["error"]["code"] == code


def test_mcp_notification_never_dispatches_a_hardware_call():
    class Acc:
        def dispatch(self, *_args):
            raise AssertionError("a notification must never dispatch")

    assert servers._mcp_reply(Acc(), {
        "jsonrpc": "2.0", "method": "tools/call",
        "params": {"name": "set_intensity", "arguments": {"intensity": 50}}}) is None


# ----- framing -----

def test_frame_round_trip_and_decoder():
    payload = json.dumps({"ping": {}})
    framed = servers.frame(payload)
    decoder = servers.FrameDecoder()
    decoder.feed(framed[:3])
    assert list(decoder.frames()) == []            # partial: nothing yet
    decoder.feed(framed[3:])
    assert [f.decode() for f in decoder.frames()] == [payload]
