"""Verify that the shipped patch loads and preserves its integration and safety contract.

The suite runs against either the patch or the canonical source. Exhaustive command semantics live
in ``impl/tests``; these checks focus on packaging, fail-closed startup, thin upstream hooks, and the
rule that Remote Control must not suppress mesoSPIM warnings.
"""

import types

import pytest

from tests.support import patch_loader
from tests.support import contracts

dispatcher = patch_loader.dispatcher
commands = patch_loader.commands
srv = patch_loader.srv
config = patch_loader.config

pytestmark = [pytest.mark.offline, pytest.mark.unit, pytest.mark.valid]


def test_patch_modules_loaded():
    assert callable(dispatcher.run)
    assert callable(commands.self_test)
    assert callable(srv.frame)
    assert callable(srv.start)  # the Core-facing entry point


def test_allowlist_is_the_documented_53():
    assert len(dispatcher.COMMANDS) == 53
    assert "set_mode" not in dispatcher.COMMANDS  # dropped: every mode has its own command
    assert "procedure" not in dispatcher.COMMANDS
    assert "snap" not in dispatcher.COMMANDS  # the remote snapshot feature was removed
    assert "get_snap_image" not in dispatcher.COMMANDS
    assert (
        "execute_stage_program" not in dispatcher.COMMANDS
    )  # dropped: opaque stage program can't be bounded
    assert set(contracts.VALID_CASES) == set(dispatcher.COMMANDS)


def test_error_codes_are_typed():
    assert dispatcher.error_info(dispatcher.ValidationError("x"))[0] == "validation"
    assert dispatcher.error_info(dispatcher.BusyError("x"))[0] == "busy"
    assert dispatcher.error_info(dispatcher.UnknownCommand("x"))[0] == "unknown_command"
    assert dispatcher.error_info(KeyError("x"))[0] == "execution"  # a plain KeyError, not unknown


def test_strict_json_and_envelope():
    name, args = dispatcher.parse_call('{"move_absolute": {"targets": {"x": 1}}}')
    assert name == "move_absolute" and args == {"targets": {"x": 1}}
    with pytest.raises(dispatcher.ValidationError):
        dispatcher.strict_json_loads('{"a": NaN}')  # non-finite numbers rejected
    with pytest.raises(dispatcher.ValidationError):
        dispatcher.parse_call('{"a": {}, "b": {}}')  # exactly one command per call


def test_mcp_identity_constants_present():
    """Guards _mcp_reply initialize: it reports both the server name and version."""
    assert config.MCP_SERVER_NAME
    assert config.MCP_SERVER_VERSION


# --- folded fail-closed: no socket binds, because self_test/mode/token checks raise first ---


def test_unknown_transport_mode_refused_before_bind():
    with pytest.raises(ValueError):
        srv.start(object(), "SMTP", "127.0.0.1", 0, "token")


def test_mcp_requires_a_token():
    with pytest.raises(ValueError):  # decision 3: never bind without a token
        srv.start(object(), "MCP", "127.0.0.1", 0, "")


def test_limitless_cfg_fails_closed_before_any_bind():
    blind = types.SimpleNamespace(cfg=types.SimpleNamespace())  # no stage limits -> self-test fails
    with pytest.raises(RuntimeError):  # raises BEFORE any Acceptor/adapter/socket
        srv.start(blind, "MCP", "127.0.0.1", 0, "tok")


def test_default_password_refused_on_non_loopback():
    # The public default password is kept for loopback use but refused on a network bind.
    with pytest.raises(ValueError):
        srv.start(object(), "MCP", "0.0.0.0", 0, config.DEFAULT_TOKEN)


# --- Invalid configurations fail closed without warning-only fallbacks ---


def _patch_changed_lines(patch):
    """The lines the patch adds or removes, without the ---/+++ file headers."""
    return [
        line[1:]
        for line in patch.splitlines()
        if (line.startswith("+") or line.startswith("-")) and not line.startswith(("+++", "---"))
    ]


def _added_lines_for(patch, path):
    marker = f"diff --git a/{path} b/{path}"
    section = patch.split(marker, 1)[1].split("\ndiff --git ", 1)[0]
    return [
        line[1:]
        for line in section.splitlines()
        if line.startswith("+") and not line.startswith("+++") and line[1:].strip()
    ]


def test_existing_mesospim_files_remain_thin_integration_points():
    patch = patch_loader.PATCH.read_text(encoding="utf-8")
    core = _added_lines_for(patch, "mesoSPIM/src/mesoSPIM_Core.py")
    main = _added_lines_for(patch, "mesoSPIM/src/mesoSPIM_MainWindow.py")
    integration = [line for line in core if "frame_queue_display" not in line]

    assert len(integration) == 11
    assert len(main) == 3
    assert any("start_for_core" in line for line in integration)
    assert any("stop_for_core" in line for line in integration)
    assert not any("try:" in line or "logger." in line for line in integration)


def test_warning_suppression_is_not_part_of_the_patch():
    """The contribution must not alter or suppress mesoSPIM's warning display."""
    patch = patch_loader.PATCH.read_text(encoding="utf-8")
    assert "show_warning" not in patch
    assert "_mesospim_remote_zoom_gui_echo" not in patch
    assert "def report_warning(self, message)" not in patch
    assert "mesoSPIM_WaveFormGenerator.py" not in patch
    assert "sig_warning.connect(self._on_core_warning" not in patch
    assert "_mesospim_remote_gui_warning_suppressions" not in patch
    assert [line for line in _patch_changed_lines(patch) if "display_warning" in line] == []
