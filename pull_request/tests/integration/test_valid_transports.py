"""Valid command contracts exercised through real loopback MCP and TCP."""
from __future__ import annotations

import base64
import hashlib
import json

import pytest

from tests.integration import test_transport_security as harness
from tests.support.contracts import EXPECTED_CORE_CALL, READ_ONLY_WITHOUT_CORE_CALL, VALID_CASES


def setup_module(_module=None):
    harness.setup_module()


def teardown_module(_module=None):
    harness.teardown_module()


def _invoke(transport, name, arguments):
    if transport == "mcp":
        status, reply = harness._mcp_tool(name, arguments)
        assert status == 200
        result = reply["result"]
        text = result["content"][0]["text"]
        return not result["isError"], json.loads(text)

    reply = harness._tcp_call({name: arguments})
    if reply.startswith(harness.srv.OK_MARKER):
        return True, json.loads(reply[len(harness.srv.OK_MARKER) :])
    return False, {"error": reply}


def test_contract_table_covers_every_allowlisted_command_exactly_once():
    assert set(VALID_CASES) == set(harness.vrc.COMMANDS)
    assert len(VALID_CASES) == 55
    classified = set(EXPECTED_CORE_CALL) | READ_ONLY_WITHOUT_CORE_CALL | {
        "set_acquisition_list",
        "acquire_finish",
    }
    assert classified == set(VALID_CASES)


def test_procedure_is_gone_from_every_surface():
    """It was advertised in COMMANDS, capabilities and tools/list, and its handler could only
    raise. A command a client can discover but never successfully call is worse than absent."""
    assert "procedure" not in harness.vrc.COMMANDS
    assert "procedure" not in harness.vrc._HINTS
    assert "procedure" not in VALID_CASES
    assert "procedure" not in harness.vrc.run(harness._core, "get_capabilities", {})["commands"]
    tools = [tool["name"] for tool in harness.vrc.tool_specs()]
    assert "procedure" not in tools


@pytest.mark.parametrize("transport", ["mcp", "tcp"])
@pytest.mark.parametrize("name", sorted(VALID_CASES))
def test_valid_command_contract_over_both_transports(transport, name):
    harness._core.reset()
    snapshot_bytes = b"snapshot-bytes"
    if name == "get_snap_image":
        harness._core._remote_session["snapshot"] = {
            "operation_id": "op-test",
            "format": "raw",
            "dtype": "<u2",
            "shape": [1, 7],
            "order": "C",
            "total_bytes": len(snapshot_bytes),
            "sha256": hashlib.sha256(snapshot_bytes).hexdigest(),
            "_data": snapshot_bytes,
        }
    ok, result = _invoke(transport, name, VALID_CASES[name])
    assert ok, (transport, name, result)
    assert isinstance(result, dict)
    call_names = [call[0] for call in harness._core.calls()]
    if name in EXPECTED_CORE_CALL:
        assert EXPECTED_CORE_CALL[name] in call_names, (transport, name, call_names)
    elif name in READ_ONLY_WITHOUT_CORE_CALL:
        assert call_names == [], (transport, name, call_names)
    elif name == "set_acquisition_list":
        assert harness._core.state["selected_row"] == 0
    elif name == "acquire_finish":
        assert result["state"] == "idle"

    if name == "move_absolute":
        assert harness._core.state["position"]["x_pos"] == 100
    elif name == "move_relative":
        assert harness._core.state["position"]["x_pos"] == 24998
    elif name == "set_intensity":
        assert harness._core.state["intensity"] == 25
    elif name == "open_shutters":
        assert result["shutterstate"] is True
    elif name == "close_shutters":
        assert result["shutterstate"] is False
    elif name == "self_test":
        assert result["ok"] is True
    elif name == "get_info":
        assert "save_path" in result and "warnings" in result
    elif name == "get_snap_image":
        assert base64.b64decode(result["data"]) == snapshot_bytes[:4]
        assert result["next_offset"] == 4 and result["complete"] is False
    elif name == "snap":
        snap_call = next(call for call in harness._core.calls() if call[0] == "snap")
        assert snap_call[2]["write_flag"] is False


@pytest.mark.parametrize("transport", ["mcp", "tcp"])
def test_set_mode_snap_routes_to_gui_free_snapshot_path(transport):
    harness._core.reset()
    ok, result = _invoke(transport, "set_mode", {"mode": "snap"})
    assert ok and result["image_stream"] == "get_snap_image"
    calls = harness._core.calls()
    snap_call = next(call for call in calls if call[0] == "snap")
    assert snap_call[2]["write_flag"] is False
    assert not any(call[0] == "set_state" for call in calls)
