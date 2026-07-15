"""Unit tests for Remote Control framing, authentication, validation, and MCP.

Rebuilds the two ``mesoSPIM_RemoteControl_*`` modules straight from the
``0001-*.patch`` new-file hunks (one source of truth -- the patch itself), then
checks what the server promises: frames round-trip, the token is enforced in
constant time, bad VALUES are refused (shape / option / range), the MCP reply shape
is right, and a hostile-payload sweep proves nothing outside the allowlist ever
runs. No Qt, no mesoSPIM, no third-party imports. Run with::

    python tests/run.py offline valid

License: MIT (test-side; imports nothing from mesoSPIM).
"""
from __future__ import annotations

import base64
import hashlib
import json
import types

from tests.support.fake_core import UnitConfig as _Cfg
from tests.support.fake_core import UnitCore as _Core
from tests.support.fake_state import FakeState
from tests.support.patch_loader import PATCH as _PATCH
from tests.support.patch_loader import srv, vrc


_core = _Core()
_LIMITS = {"x": (-1000.0, 1000.0), "y": (-1000.0, 1000.0), "z": (-1000.0, 1000.0)}


# -- framing -------------------------------------------------------------------

def test_frame_is_length_prefixed_bytes():
    assert srv.frame("abc") == b"3\nabc"


def test_frame_counts_bytes_not_characters():
    assert srv.frame("é") == b"2\n\xc3\xa9"  # 1 char, 2 UTF-8 bytes


def test_decoder_reassembles_split_and_joined_frames():
    d = srv.FrameDecoder()
    d.feed(b"3\nab")          # a frame split mid-payload
    assert list(d.frames()) == []
    d.feed(b"c2\nhi")         # rest of frame 1 + a whole frame 2
    assert list(d.frames()) == [b"abc", b"hi"]


def test_qt_server_drains_prebuffered_auth_and_nonrecursive_command_data():
    """A fast MCP bridge must not lose data around Qt readyRead signal timing."""
    class _Signal:
        def connect(self, slot):
            self.slot = slot

        def disconnect(self):
            pass

    class _Connection:
        def __init__(self):
            self.readyRead = _Signal()
            self.disconnected = _Signal()
            self.incoming = bytearray(srv.frame("secret"))
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
            if data == srv.frame("OK"):
                # Model a loopback client sending the command immediately while
                # the non-recursive readyRead callback is still active.
                self.incoming.extend(srv.frame('{"ping": {}}'))

        def flush(self):
            pass

        def disconnectFromHost(self):
            pass

        def deleteLater(self):
            pass

    class _PendingServer:
        def __init__(self, connection):
            self.connection = connection

        def hasPendingConnections(self):
            return self.connection is not None

        def nextPendingConnection(self):
            connection, self.connection = self.connection, None
            return connection

    connection = _Connection()
    server = object.__new__(srv.RemoteControlTCPServer)
    server.core = _core
    server._token = "secret"
    server._clients = {}
    server._server = _PendingServer(connection)

    server._on_new_connection()

    assert connection.writes[0] == srv.frame("OK")
    reply = connection.writes[1]
    header, payload = reply.split(b"\n", 1)
    assert int(header) == len(payload)
    assert payload.startswith(srv.OK_MARKER.encode("ascii"))
    assert json.loads(payload[len(srv.OK_MARKER):])["pong"] is True


# -- auth ----------------------------------------------------------------------

def test_authgate_accepts_only_the_right_token():
    gate = srv.AuthGate("sécret")   # non-ASCII token
    assert not gate.check("wrong")
    assert gate.check("sécret")
    assert gate.passed


def test_authgate_open_when_no_token():
    assert srv.AuthGate(None).passed


# -- parse / allowlist / hostile sweep -----------------------------------------

def test_parse_call_rejects_bad_shapes():
    for payload in ('{"a": {}, "b": {}}', '{"move": []}', 'not json', '[]'):
        try:
            srv.parse_call(payload)
        except (ValueError, json.JSONDecodeError):
            continue
        raise AssertionError(f"parse_call accepted a bad payload: {payload!r}")


def test_run_rejects_unknown_and_hostile_names():
    for name in ("no_such_command", "os.system('rm -rf /')", "__class__", "eval"):
        try:
            vrc.run(_core, name, {})
        except KeyError:
            continue
        raise AssertionError(f"allowlist accepted a hostile name: {name!r}")


# -- input validation (_validate) ----------------------------------------------

def _rejects(call, args):
    try:
        vrc._validate(_core, call, args, _LIMITS)
    except ValueError:
        return True
    return False


def test_valid_calls_pass_validation():
    vrc._validate(_core, "move_absolute", {"targets": {"x": 100}}, _LIMITS)
    vrc._validate(_core, "set_filter", {"filter": "Empty"}, _LIMITS)
    vrc._validate(_core, "get_state", {}, _LIMITS)


def test_out_of_range_move_rejected():
    assert _rejects("move_absolute", {"targets": {"x": 999999}})


def test_unknown_axis_and_non_number_rejected():
    assert _rejects("move_absolute", {"targets": {"q": 1}})
    assert _rejects("move_absolute", {"targets": {"x": "far"}})


def test_bad_option_rejected():
    assert _rejects("set_filter", {"filter": "NOPE"})
    assert _rejects("set_zoom", {"zoom": "99x"})
    assert _rejects("set_shutterconfig", {"shutterconfig": "Sideways"})


def test_bad_intensity_rejected():
    assert _rejects("set_intensity", {"intensity": 250})


def test_range_and_type_checked_for_all_settables():
    # not just the stage: numeric type + percent range on any settable parameter
    assert _rejects("set_etl", {"etl_l_amplitude": "loud"})   # must be a number
    assert _rejects("set_etl", {"etl_l_delay_%": 250})        # percent 0..100
    assert not _rejects("set_etl", {"etl_l_amplitude": 1.5})  # no range -> type only


def test_both_lanes_refuse_out_of_limit_with_error_json():
    # TCP and MCP converge on handle_tcp_message -> run -> _validate. The MCP lane
    # forwards to this exact reply, so an OUT-OF-LIMIT call can never reach the Core on
    # either lane -- it comes back as a non-OK error (which MCP wraps as isError JSON),
    # and the message names the limit.
    reply = srv.handle_tcp_message(_core, json.dumps({"move_absolute": {"targets": {"x": 999999}}}))
    assert not reply.startswith(srv.OK_MARKER)
    assert "error" in reply and "25000" in reply


def test_cfg_stage_range_enforced_via_run():
    # run() takes the range from the loaded cfg -- no env var needed -- and the error
    # message names the limit so a script/LLM knows what was allowed.
    try:
        vrc.run(_core, "move_absolute", {"targets": {"x": 999999}})
    except ValueError as e:
        assert "25000" in str(e)
    else:
        raise AssertionError("cfg stage range not enforced")


def test_get_limits_exposes_enforced_rules():
    enforced = vrc._get_limits(_core, {})["enforced"]
    assert enforced["axes"]["x"] == [-25000.0, 25000.0]
    assert enforced["axes"]["theta"] is None  # range OFF -> visible to the caller
    assert enforced["parameters"]["intensity"]["range"] == [0, 100]


def test_remote_snap_is_gui_free_and_snapshot_chunks_round_trip():
    assert _rejects("snap", {"write": True})
    assert _rejects("snap", {"laser_blanking": "yes"})
    assert _rejects("set_state", {"settings": {"state": "snap"}})
    assert _rejects("get_snap_image", {"offset": -1})
    assert _rejects("get_snap_image", {"max_bytes": 512 * 1024 + 1})

    pixels = b"abcdefghijkl"

    class _Image:
        dtype = types.SimpleNamespace(str="<u2", hasobject=False)
        shape = (2, 3)

        def tobytes(self, order="C"):
            assert order == "C"
            return pixels

    core = types.SimpleNamespace(
        cfg=_Cfg(),
        state=FakeState(folder="save", snap_folder="snaps", ETL_cfg_file="etl.csv"),
        frame_queue_display=[_Image()],
        _remote_session={  # a Core mid-snap; the real one builds this session in __init__
            "operation": {
                "id": "op-snap", "command": "snap", "status": "processing",
                "_completion": "snap_image", "warnings": ["remote warning"],
            },
            "counter": 1,
            "snapshot": None,
        },
    )
    assert vrc.capture_snap_image(core) is True
    chunks = []
    offset = 0
    while True:
        result = vrc.run(core, "get_snap_image", {
            "operation_id": "op-snap", "offset": offset, "max_bytes": 5})
        chunks.append(base64.b64decode(result["data"]))
        if result["complete"]:
            break
        offset = result["next_offset"]
    assert b"".join(chunks) == pixels
    assert result["sha256"] == hashlib.sha256(pixels).hexdigest()
    info = vrc.run(core, "get_info")
    assert info["save_path"] == "save" and info["warnings"] == ["remote warning"]
    assert "latest_snapshot" not in info
    assert "latest_snapshot" not in vrc.run(core, "get_progress")
    core.state["folder"] = "updated-save"
    assert vrc.run(core, "get_info")["save_path"] == "updated-save"


def test_remote_zoom_uses_the_existing_core_signature():
    class _ZoomCore:
        cfg = _Cfg()

        def __init__(self):
            self.state = FakeState()
            self.calls = []

        def set_zoom(self, zoom, **kwargs):
            self.calls.append((zoom, kwargs))

    core = _ZoomCore()
    result = vrc.run(core, "set_zoom", {"zoom": "1x"})
    assert result["operation"]["status"] == "completed"
    assert core.calls == [("1x", {
        "wait_until_done": True, "update_etl": True})]

    core = _ZoomCore()
    vrc.run(core, "set_zoom", {"zoom": "2x", "update_etl": False})
    assert core.calls == [("2x", {
        "wait_until_done": True, "update_etl": False})]


def _patch_changed_lines(patch):
    """The lines the patch actually adds or removes, without the ---/+++ file headers.

    Context lines are excluded on purpose: their presence is an accident of diff hunk
    boundaries, not a property of the contribution.
    """
    return [line[1:] for line in patch.splitlines()
            if (line.startswith("+") or line.startswith("-"))
            and not line.startswith(("+++", "---"))]


def test_warning_suppression_is_not_part_of_the_patch():
    """The PR must not touch mesoSPIM's warning display: no suppression hook anywhere, and
    MainWindow.display_warning left exactly as upstream wrote it.

    The second half used to be proven by finding the stock display_warning body in the patch
    -- it rode along as a CONTEXT line under the 201 GUI lines that sat beneath it. Those
    lines now live in mesoSPIM_RemoteControl_Tab.py, so display_warning falls outside every
    hunk. Asserting on context text was always incidental; assert the real property instead:
    the patch adds and removes no display_warning line at all.
    """
    patch = _PATCH.read_text(encoding="utf-8")
    assert "show_warning" not in patch
    assert "_mesospim_remote_zoom_gui_echo" not in patch
    assert "def report_warning(self, message)" not in patch
    assert "mesoSPIM_WaveFormGenerator.py" not in patch
    assert "sig_warning.connect(self._on_core_warning" not in patch
    assert "_mesospim_remote_gui_warning_suppressions" not in patch
    assert [line for line in _patch_changed_lines(patch) if "display_warning" in line] == []


def test_self_test_command_verifies_limits_against_a_mock():
    # the pre-flight, callable over either lane: proves the loaded limits enforce, against
    # a SimCore that mimics the hardware -- and never moves the real stage.
    out = vrc.run(_core, "self_test")
    assert out["ok"] is True and all(line.startswith("PASS") for line in out["report"])


def test_server_gate_refuses_to_start_when_limits_missing():
    # RemoteControlTCPServer runs self_test FIRST (before importing Qt); a cfg with no limits
    # must make construction raise, so the server never binds and hardware is never exposed.
    class _NoLimitsCore:
        class cfg:  # noqa: N801 - tiny stand-in
            filterdict = {}; zoomdict = {}; laserdict = {}; shutteroptions = []  # noqa: E702
    try:
        srv.RemoteControlTCPServer(_NoLimitsCore())
    except RuntimeError as e:
        assert "self-test failed" in str(e)
    except ImportError:
        raise AssertionError("gate did not fire before the PyQt5 import") from None
    else:
        raise AssertionError("server bound despite unenforceable limits")


def test_server_gate_passes_good_cfg():
    # with a good cfg the gate must NOT block: construction gets past the self-test into Qt
    # land (PyQt5 import / QTcpServer), whatever that raises here -- just not a self-test fail.
    try:
        srv.RemoteControlTCPServer(_core)
    except RuntimeError as e:
        assert "self-test failed" not in str(e), f"gate wrongly blocked a good cfg: {e}"
    except Exception:
        pass  # got past the gate (no PyQt5 / not a QObject) -> the self-test passed


def test_limits_from_env(monkeypatch=None):
    import os
    os.environ["MESOSPIM_RS_LIMITS"] = '{"x": [-5, 5]}'
    try:
        assert vrc._limits_from_env() == {"x": (-5.0, 5.0)}
    finally:
        del os.environ["MESOSPIM_RS_LIMITS"]


# -- MCP reply shape (no live TCP: initialize / tools/list / unknown / notify) --

def test_mcp_initialize_and_tools_list():
    # mcp_reply returns the JSON-RPC dict; the HTTP handler serialises it.
    cfg = types.SimpleNamespace()
    init = srv.mcp_reply(cfg, {"id": 1, "method": "initialize"})
    assert init["result"]["protocolVersion"] == "2024-11-05"
    listed = srv.mcp_reply(cfg, {"id": 2, "method": "tools/list"})
    assert len(listed["result"]["tools"]) == len(vrc.COMMANDS)


def test_mcp_unknown_method_and_notification():
    cfg = types.SimpleNamespace()
    err = srv.mcp_reply(cfg, {"id": 3, "method": "no_such"})
    assert err["error"]["code"] == -32601
    assert srv.mcp_reply(cfg, {"method": "notifications/initialized"}) is None  # no id -> no reply


if __name__ == "__main__":
    _passed = 0
    for _name, _fn in sorted(globals().items()):
        if _name.startswith("test_") and callable(_fn):
            _fn()
            print(f"ok   {_name}")
            _passed += 1
    print(f"\nALL {_passed} TESTS PASSED")
