"""AI Assistant worker logic, from source, under the Qt-free shim in conftest.

Covers the completion wrapper (dispatch_and_wait), the tool builder, and the worker's turn,
retry, tool-surfacing, and interrupt behaviour with a fake agent — no live model, no hardware.
Real-thread ordering is left to the real-PyQt smoke test, matching the Remote Control split.
"""
import json
import threading

import pytest

from mesoSPIM.src import mesoSPIM_AiAssistent as ai
from mesoSPIM.src.mesoSPIM_AiAssistent import (
    AssistantWorker, dispatch_and_wait, start_assistant_for_core, stop_assistant_for_core)
from mesoSPIM.src.mesoSPIM_RemoteControl_Dispatcher import READ, WAIT, COMPLETED
from fakes import FakeCore


# --- dispatch_and_wait: the completion wrapper ---

class FakeAcceptor:
    """Scripts dispatch() with the REAL nesting: status/id live under "operation". A WAIT op
    reports 'processing' then 'completed' after `flip_after` get_progress polls."""

    def __init__(self, flip_after=2):
        self.calls = []
        self._polls = 0
        self._flip_after = flip_after

    def dispatch(self, name, args):
        self.calls.append((name, args))
        if name == "get_progress":
            self._polls += 1
            status = COMPLETED if self._polls >= self._flip_after else "processing"
            return {"operation": {"status": status, "id": "op-000001"}}
        return {"accepted": True, "operation": {"id": "op-000001", "status": "processing"}}


class _Cfg:
    POLL_INTERVAL_S = 0.0
    WAIT_CAP_S = 5


def test_read_returns_immediately():
    acc = FakeAcceptor()
    dispatch_and_wait(acc, "get_state", {}, READ, threading.Event(), _Cfg)
    assert acc.calls == [("get_state", {})]                       # no polling for a READ


def test_wait_blocks_until_completed():
    acc = FakeAcceptor(flip_after=3)
    out = dispatch_and_wait(acc, "move_absolute", {"targets": {"x": 12000}}, WAIT, threading.Event(), _Cfg)
    assert out["status"] == COMPLETED
    assert [c[0] for c in acc.calls].count("get_progress") == 3


def test_cancel_before_dispatch_actuates_nothing():
    acc = FakeAcceptor()
    cancel = threading.Event()
    cancel.set()
    out = dispatch_and_wait(acc, "move_absolute", {"targets": {"x": 1}}, WAIT, cancel, _Cfg)
    assert out["status"] == "cancelled"
    assert acc.calls == []                                         # gated before any dispatch


def test_wait_returns_still_running_past_cap():
    acc = FakeAcceptor(flip_after=10**9)                          # genuinely never completes

    class Cfg:
        POLL_INTERVAL_S = 0.0
        WAIT_CAP_S = 0.05

    out = dispatch_and_wait(acc, "run_acquisition_list", {}, WAIT, threading.Event(), Cfg)
    assert out["status"] == "still_running"


def test_build_tools_covers_every_command():
    pytest.importorskip("pydantic_ai")
    from mesoSPIM.src.mesoSPIM_AiAssistent import build_tools
    from mesoSPIM.src.mesoSPIM_RemoteControl_Dispatcher import COMMANDS
    tools = build_tools(FakeAcceptor(), threading.Event())
    assert len(tools) == len(COMMANDS)


# --- the worker: turn, retry, tool-surfacing, interrupt (fake agent) ---

class FakeResult:
    def __init__(self, output):
        self.output = output

    def all_messages(self):
        return ["history"]


class FakeAgent:
    def __init__(self, results=None, errors=None):
        self._results = list(results or [])
        self._errors = list(errors or [])
        self.runs = 0

    def run_sync(self, text, message_history=None):
        self.runs += 1
        if self._errors:
            error = self._errors.pop(0)
            if error is not None:
                raise error
        return self._results.pop(0) if self._results else FakeResult("ok")


def _collect(signal):
    got = []
    signal.connect(lambda *a: got.append(a[0] if len(a) == 1 else a))
    return got


def test_run_turn_emits_reply(monkeypatch):
    worker = AssistantWorker(FakeAcceptor())
    monkeypatch.setattr(ai, "build_agent", lambda a, c, on_call=None: FakeAgent([FakeResult("moved")]))
    replies = _collect(worker.sig_reply)
    dones = _collect(worker.sig_done)
    worker.run_turn("go")
    assert replies == ["moved"]
    assert len(dones) == 1


def test_tool_fn_streams_call_before_dispatch():
    acc = FakeAcceptor()
    seen = []
    tool = ai._tool_fn(acc, "get_state", READ, threading.Event(), on_call=lambda n, a: seen.append((n, a)))
    out = tool({"foo": 1})
    assert seen == [("get_state", json.dumps({"foo": 1}))]      # surfaced live, at the tool boundary
    assert ("get_state", {"foo": 1}) in acc.calls               # then dispatched
    assert "accepted" in out


def test_run_turn_error_emits_sig_error(monkeypatch):
    worker = AssistantWorker(FakeAcceptor())
    monkeypatch.setattr(ai, "build_agent", lambda a, c, on_call=None: FakeAgent(errors=[RuntimeError("boom")]))
    errors = _collect(worker.sig_error)
    dones = _collect(worker.sig_done)
    worker.run_turn("go")
    assert errors and "boom" in errors[0]
    assert len(dones) == 1                                        # sig_done fires even on failure


def test_retry_on_503_then_success(monkeypatch):
    worker = AssistantWorker(FakeAcceptor())
    transient = RuntimeError("status_code: 503 UNAVAILABLE")
    worker._agent = FakeAgent(results=[FakeResult("ok")], errors=[transient, transient])
    monkeypatch.setattr(ai.time, "sleep", lambda *_: None)
    result = worker._run_with_retry("go")
    assert result.output == "ok"
    assert worker._agent.runs == 3                                # two 503s retried, third succeeds


def test_retry_gives_up_on_non_transient(monkeypatch):
    worker = AssistantWorker(FakeAcceptor())
    worker._agent = FakeAgent(errors=[ValueError("bad request")])
    monkeypatch.setattr(ai.time, "sleep", lambda *_: None)
    with pytest.raises(ValueError):
        worker._run_with_retry("go")
    assert worker._agent.runs == 1                                # non-transient: no retry


def test_interrupt_sets_cancel_and_stops():
    acc = FakeAcceptor()
    worker = AssistantWorker(acc)
    worker.interrupt()
    assert worker.cancel.is_set()
    assert ("stop", {}) in acc.calls


# --- Acceptor lifecycle for Core (start/stop_assistant_for_core) ---

def test_start_assistant_builds_and_reuses_one_acceptor():
    core = FakeCore()
    core._remote_control = None
    acceptor = start_assistant_for_core(core)                 # passes self_test, builds an Acceptor
    assert acceptor is not None
    assert core._assistant_acceptor is acceptor
    assert start_assistant_for_core(core) is acceptor         # idempotent: one Acceptor per session


def test_start_assistant_refused_while_transport_runs():
    core = FakeCore()
    core._remote_control = object()                           # a transport holds the session
    assert start_assistant_for_core(core) is None
    assert core._assistant_acceptor is None


def test_stop_assistant_releases_the_acceptor():
    core = FakeCore()
    core._remote_control = None
    start_assistant_for_core(core)
    stop_assistant_for_core(core)
    assert core._assistant_acceptor is None
