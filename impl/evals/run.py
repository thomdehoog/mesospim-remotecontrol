"""Score the suite and write a report.

Each case gets a FRESH backend, so cases cannot contaminate each other through stage position or
a latched setting. Free-tier quota is the binding constraint, not wall-clock: one case costs one
model request per tool call plus one, the daily allowance is a few hundred, so runs are sequential,
paced under the RPM limit, and stoppable with --budget.

    python -m evals.run --smoke              # no model calls: prove the plumbing
    python -m evals.run --live               # score the suite
    python -m evals.run --live --only unit-mm,ambiguous-move --budget 20

Maintainer (2026):
    Thom de Hoog
    Center for Microscopy and Image Analysis
    thom.dehoog@zmb.uzh.ch
    thomdehoog@gmail.com
"""

import argparse
import json
import threading
import time
from collections import Counter
from pathlib import Path

from evals import cases as suite
from evals.harness import ensure_key, fake_backend, run_turn

REPORTS = Path(__file__).resolve().parent / "reports"


def read_state(core, path):
    """Dotted read over the FakeState mapping: 'position.x_pos' -> core.state['position']['x_pos']."""
    head, _, tail = path.partition(".")
    value = core.state[head]
    for part in filter(None, tail.split(".")):
        value = value[part]
    return value


def check(case, turn, core):
    """Every way this case can fail, as human-readable lines. Empty list == pass."""
    failures = []
    if turn.error:
        failures.append(f"turn raised {turn.error}")
    attempted, landed = turn.tool_names, [name for name, _, _ in core.calls]

    for name in case.get("expect_tools", []):
        if name not in attempted:
            failures.append(f"expected tool {name}, attempted {attempted or 'nothing'}")
    for name in case.get("forbid_tools", []):
        if name in attempted:
            failures.append(f"forbidden tool {name} was called")
    for name in case.get("expect_core_calls", []):
        if name not in landed:
            failures.append(f"expected hardware call {name}, landed {landed or 'nothing'}")
    for name in case.get("forbid_core_calls", []):
        if name in landed:
            failures.append(f"forbidden hardware call {name} reached the microscope")
    for path, expected in case.get("expect_state", {}).items():
        try:
            actual = read_state(core, path)
        except (KeyError, TypeError):
            failures.append(f"state {path} missing")
            continue
        if isinstance(expected, float) and abs(float(actual) - expected) > 1e-6:
            failures.append(f"state {path} = {actual}, expected {expected}")
        elif not isinstance(expected, float) and actual != expected:
            failures.append(f"state {path} = {actual!r}, expected {expected!r}")
    cap = case.get("max_tools")
    if cap is not None and len(attempted) > cap:
        failures.append(f"{len(attempted)} tool calls exceeds the {cap} allowed: {attempted}")
    return failures


class Pacer:
    """Hold the run under the endpoint's requests-per-minute limit. The daily allowance is spent
    slowly enough that a 429 mid-suite would cost more than the wait it replaces."""

    def __init__(self, rpm):
        self._min_gap = 60.0 / max(rpm, 1)
        self._last = 0.0

    def wait_for(self, requests):
        due = self._last + self._min_gap * max(requests, 1)
        pause = due - time.monotonic()
        if pause > 0:
            time.sleep(pause)
        self._last = time.monotonic()


def smoke():
    """Prove every non-model link: shim, real Acceptor, tool build, dispatch, state change.
    Costs nothing and catches the harness breaking without spending a single request."""
    from mesoSPIM.src.mesoSPIM_AiAssistent import build_system_prompt, build_tools

    acceptor, core = fake_backend()
    tools = build_tools(acceptor, threading.Event())
    prompt = build_system_prompt(acceptor)
    by_name = {tool.name: tool for tool in tools}
    reply = by_name["move_absolute"].function(targets={"x": 1234.0})
    landed = read_state(core, "position.x_pos")
    print(f"tools={len(tools)}  system prompt={len(prompt)} chars  "
          f"dispatch->core x_pos={landed}")
    assert landed == 1234.0, f"a dispatched move did not reach the fake core: {reply}"
    print("smoke: OK")


def score(selected, budget, rpm):
    """Run each case on a fresh backend and collect the report rows."""
    pacer, rows, spent = Pacer(rpm), [], 0
    for case in selected:
        if budget and spent >= budget:
            print(f"-- budget of {budget} requests reached, stopping --")
            break
        acceptor, core = fake_backend()
        pacer.wait_for(case.get("max_tools", 3))
        turn = run_turn(acceptor, case["prompt"])
        spent += turn.requests
        failures = check(case, turn, core)
        rows.append({"id": case["id"], "category": case["category"], "prompt": case["prompt"],
                     "passed": not failures, "failures": failures,
                     "tools": turn.tools, "core_calls": [name for name, _, _ in core.calls],
                     "reply": turn.reply, "error": turn.error,
                     "seconds": turn.seconds, "requests": turn.requests})
        mark = "PASS" if not failures else "FAIL"
        print(f"[{mark}] {case['id']:<26} {turn.tool_names or '(no tools)'}")
        for line in failures:
            print(f"       - {line}")
    return rows, spent


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--smoke", action="store_true", help="no model calls; verify the plumbing")
    parser.add_argument("--live", action="store_true", help="score the suite against the model")
    parser.add_argument("--only", default="", help="comma-separated case ids")
    parser.add_argument("--budget", type=int, default=0, help="stop after N model requests")
    parser.add_argument("--rpm", type=int, default=12, help="requests per minute ceiling")
    args = parser.parse_args()

    if args.smoke or not args.live:
        smoke()
        if not args.live:
            return

    if not ensure_key():
        raise SystemExit("no API key: set GEMINI_API_KEY or MESOSPIM_AGENT_KEY_FILE")

    wanted = {name for name in args.only.split(",") if name}
    selected = [c for c in suite.CASES if not wanted or c["id"] in wanted]
    started = time.time()
    rows, spent = score(selected, args.budget, args.rpm)

    passed = sum(row["passed"] for row in rows)
    by_category = Counter(row["category"] for row in rows if not row["passed"])
    report = {"generated": time.strftime("%Y-%m-%dT%H:%M:%S", time.localtime(started)),
              "passed": passed, "total": len(rows), "requests": spent,
              "failing_categories": dict(by_category), "cases": rows}
    REPORTS.mkdir(exist_ok=True)
    path = REPORTS / f"{time.strftime('%Y%m%d-%H%M%S', time.localtime(started))}.json"
    path.write_text(json.dumps(report, indent=2), encoding="utf-8")

    print(f"\n{passed}/{len(rows)} passed, {spent} requests spent")
    if by_category:
        print("failing categories: " + ", ".join(f"{k} ({v})" for k, v in by_category.items()))
    print(f"report: {path}")


if __name__ == "__main__":
    main()
