"""Shared live-test gates, clients, polling, and Demo acquisition data."""
from __future__ import annotations

import json
import os
import time
import urllib.parse
from pathlib import Path

import pytest

from tests.support.clients import mcp_call


def live_config(run_gate="MESOSPIM_RUN_ALL_COMMANDS"):
    required = {
        "MESOSPIM_ALLOW_DEVICE_CHANGE": "1",
        "MESOSPIM_OPERATOR_PRESENT": "1",
        "MESOSPIM_CONFIRM_DEMO_MODE": "1",
        run_gate: "1",
    }
    for name, expected in required.items():
        if os.environ.get(name) != expected:
            pytest.skip(f"set {name}={expected} to permit the DemoStage test")

    url = os.environ.get("MESOSPIM_LIVE_MCP_URL", "http://127.0.0.1:42100/mcp")
    parsed = urllib.parse.urlparse(url)
    if (
        parsed.scheme != "http"
        or parsed.hostname not in {"127.0.0.1", "localhost"}
        or parsed.path != "/mcp"
        or parsed.port is None
    ):
        raise ValueError("live tests require a loopback http:// MCP URL ending in /mcp")

    hold = float(os.environ.get("MESOSPIM_DEMO_COMMAND_HOLD_SECONDS", "0.25"))
    if not 0 <= hold <= 1:
        raise ValueError("MESOSPIM_DEMO_COMMAND_HOLD_SECONDS must be between 0 and 1")
    request_timeout = float(os.environ.get("MESOSPIM_NETWORK_TIMEOUT_SECONDS", "10"))
    if not 0.1 <= request_timeout <= 60:
        raise ValueError("MESOSPIM_NETWORK_TIMEOUT_SECONDS must be between 0.1 and 60")

    demo_root_text = os.environ.get("MESOSPIM_DEMO_ROOT")
    etl_path_text = os.environ.get("MESOSPIM_DEMO_ETL_CONFIG_PATH")
    process_id_text = os.environ.get("MESOSPIM_DEMO_PROCESS_ID")
    if not demo_root_text or not etl_path_text:
        pytest.skip("set MESOSPIM_DEMO_ROOT and MESOSPIM_DEMO_ETL_CONFIG_PATH")
    if not process_id_text or not process_id_text.isdigit():
        pytest.skip("set MESOSPIM_DEMO_PROCESS_ID to the current -D mesoSPIM process")
    demo_root = Path(demo_root_text).resolve()
    etl_path = Path(etl_path_text).resolve()
    if not etl_path.is_file() or not etl_path.is_relative_to(demo_root):
        raise ValueError("demo ETL config must be an existing file inside MESOSPIM_DEMO_ROOT")
    return (
        parsed.hostname,
        parsed.port,
        os.environ.get("MESOSPIM_LIVE_MCP_TOKEN"),
        hold,
        request_timeout,
        demo_root,
        etl_path,
        int(process_id_text),
    )


def raw_mcp_tool(host, port, token, request_timeout, name, arguments=None):
    reply = mcp_call(
        host,
        port,
        token,
        "tools/call",
        name,
        arguments or {},
        timeout=request_timeout,
    )
    result = reply["result"]
    text = result["content"][0]["text"]
    try:
        payload = json.loads(text)
    except json.JSONDecodeError:
        payload = {"text": text}
    return not result.get("isError", False), payload


def raw_tcp_tool(client, name, arguments=None):
    try:
        return True, client.call(name, **(arguments or {}))
    except RuntimeError as exc:
        return False, {"error": str(exc)}


def must(tool, name, arguments=None):
    ok, result = tool(name, arguments or {})
    if not ok:
        raise AssertionError(f"{name} failed: {result}")
    return result


def wait_until(predicate, label):
    """Poll observable state without guessing an operation duration."""
    while True:
        result = predicate()
        if result:
            return result
        time.sleep(0.05)


def wait_for_operation(tool, result, label):
    """Poll reported status; never guess completion from elapsed operation time."""
    if not isinstance(result, dict) or result.get("accepted") is not True:
        return None
    operation = result.get("operation") or {}
    operation_id = operation.get("id")
    if not operation_id or operation.get("status") == "completed":
        return operation
    if operation.get("status") == "failed":
        raise AssertionError(f"{label} failed: {operation}")
    def terminal():
        current = must(tool, "get_progress")["operation"]
        if current.get("id") != operation_id:
            raise AssertionError(
                f"{label} operation changed from {operation_id} to {current.get('id')}"
            )
        if current.get("status") == "failed":
            raise AssertionError(f"{label} failed: {current}")
        return current if current.get("status") == "completed" else None

    return wait_until(terminal, f"{label} operation {operation_id}")


def different(options, current):
    for option in options:
        if option != current:
            return option
    raise AssertionError(f"no alternate option available for {current!r}")


def bounded_delta(value, low, high, delta):
    candidate = float(value) + delta
    if candidate > high:
        candidate = float(value) - delta
    if not low <= candidate <= high or candidate == value:
        candidate = (low + high) / 2
    return candidate


def demo_acquisition(folder, filename, state):
    position = state["position"]
    return {
        "x_pos": position["x_pos"],
        "y_pos": position["y_pos"],
        "z_start": position["z_pos"],
        "z_end": position["z_pos"],
        "z_step": 1,
        "planes": 1,
        "rot": position["theta_pos"],
        "f_start": position["f_pos"],
        "f_end": position["f_pos"],
        "laser": state["laser"],
        "intensity": min(float(state["intensity"]), 10),
        "filter": state["filter"],
        "zoom": state["zoom"],
        "shutterconfig": state["shutterconfig"],
        "folder": str(folder),
        "filename": filename,
        "image_writer_plugin": "RAW_Writer",
        "etl_l_offset": state["etl_l_offset"],
        "etl_l_amplitude": state["etl_l_amplitude"],
        "etl_r_offset": state["etl_r_offset"],
        "etl_r_amplitude": state["etl_r_amplitude"],
        "processing": "MAX",
    }
