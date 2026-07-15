"""Small command-line front end for the Remote Control test profiles."""
from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path


TESTS = Path(__file__).resolve().parent
ROOT = TESTS.parent


def _source_line():
    """Name the code actually under test, so `run.py offline` shows patch-vs-source at a glance."""
    if str(ROOT) not in sys.path:
        sys.path.insert(0, str(ROOT))
    from tests.support import patch_loader
    return f"remote-control modules under test: {patch_loader.SOURCE}"


def _profile(scope, kind, transport):
    if scope == "offline":
        if transport is not None:
            raise ValueError("offline tests already exercise both MCP and TCP")
        print(_source_line(), flush=True)
        paths = [
            TESTS / "test_patch_smoke.py",
            TESTS / "integration" / "test_transport_matrix.py",
            TESTS / "integration" / "test_busy_gate.py",
            TESTS / "integration" / "test_transport_security.py",
        ]
        return paths, {}

    if kind == "all":
        raise ValueError("run live valid and live adversarial separately")
    if transport is None:
        raise ValueError("live tests require mcp or tcp (a session hosts one transport)")
    if transport == "both":
        raise ValueError("a live session hosts one transport; run mcp, then tcp")

    if kind == "valid":
        test_name = {
            "mcp": "test_live_mcp_x_move_changes_position_and_restores_it",
            "tcp": "test_live_tcp_x_move_changes_position_and_restores_it",
        }[transport]
        paths = [
            f"{TESTS / 'live' / 'test_valid.py'}::{test_name}",
            TESTS / "live" / "test_all_commands.py",
        ]
        return paths, {"MESOSPIM_LIVE_DEMO_TRANSPORT": transport}

    paths = [TESTS / "live" / "test_adversarial.py"]
    return paths, {"MESOSPIM_LIVE_ADVERSARIAL_TRANSPORT": transport}


def main(argv=None):
    parser = argparse.ArgumentParser(description="Run one mesoSPIM Remote Control test profile")
    parser.add_argument("scope", choices=("offline", "live"))
    parser.add_argument("kind", choices=("valid", "adversarial", "all"))
    parser.add_argument("transport", nargs="?", choices=("mcp", "tcp", "both"))
    args = parser.parse_args(argv)
    try:
        paths, additions = _profile(args.scope, args.kind, args.transport)
    except (KeyError, ValueError) as exc:
        parser.error(str(exc))

    environment = os.environ.copy()
    environment.pop("PYTEST_ADDOPTS", None)
    environment.update(additions)
    selector = {
        ("offline", "all"): "offline",
        ("offline", "valid"): "offline",
        ("offline", "adversarial"): "offline",
        ("live", "valid"): "live and valid",
        ("live", "adversarial"): "live and adversarial",
    }[(args.scope, args.kind)]
    command = [
        sys.executable, "-m", "pytest", *map(str, paths),
        "--strict-markers", "-m", selector, "-q",
    ]
    if args.scope == "live":
        command.append("-s")
    print(
        f"Running: {args.scope} {args.kind}" + (f" {args.transport}" if args.transport else ""),
        flush=True,
    )
    return subprocess.call(command, cwd=ROOT, env=environment)


if __name__ == "__main__":
    raise SystemExit(main())
