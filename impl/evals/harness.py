"""Headless driver for the AI Assistant: run one prompt and record what the agent actually did.

The agent's only coupling to the microscope is ``acceptor.dispatch(name, args)``, so driving it
outside the GUI needs neither a Qt event loop nor hardware. The ``fake`` backend runs the REAL
Acceptor / Dispatcher / Commands over a FakeCore under the offline Qt shim, so a turn stays
instant and free while still exercising accept-validation, movement limits and the busy gate —
the three things that decide whether the agent's guess actually reaches the instrument.

Ground truth for a turn is deliberately doubled: ``turn.tools`` is what the agent TRIED (the same
on_call hook the GUI streams from) and ``core.calls`` is what survived the dispatcher and reached
the hardware. A case that must be refused asserts on the second, never the first — the agent is
allowed to propose an illegal move, it is not allowed to land one.

Maintainer (2026):
    Thom de Hoog
    Center for Microscopy and Image Analysis
    thom.dehoog@zmb.uzh.ch
    thomdehoog@gmail.com
"""

import os
import sys
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path

_IMPL = Path(__file__).resolve().parent.parent


def bootstrap():
    """Install the Qt-free shim and put ``impl/`` on the path, exactly as the offline tests do.
    Must run before anything imports PyQt5 — the shim declines to replace a real Qt already
    loaded, and a real Qt without an event loop would never fire the deferred WAIT bodies."""
    tests = _IMPL / "tests"
    if str(tests) not in sys.path:
        sys.path.insert(0, str(tests))
    import conftest                                          # noqa: F401  (import installs the shim)


bootstrap()   # at import, so importing this module is enough to make any later mesoSPIM import offline


def fake_backend(**state_values):
    """A real Acceptor over a recording FakeCore. Returns (acceptor, core); read `core.calls`
    for the hardware calls that actually landed and `core.state` for the resulting state."""
    from fakes import FakeCore
    from mesoSPIM.src.mesoSPIM_AiAssistent import start_assistant_for_core

    core = FakeCore(**state_values)
    core._remote_control = None
    acceptor = start_assistant_for_core(core)
    if acceptor is None:
        raise RuntimeError("assistant self-test refused the fake core")
    return acceptor, core


def ensure_key():
    """Config reads the key from the environment; for unattended runs also accept a key file, so
    the secret never lands in shell history, the repo, or a report. Returns True if a key is set."""
    from mesoSPIM.src import mesoSPIM_AiAssistent_Config as config

    if not config.KEY_ENV:
        return True
    if os.environ.get(config.KEY_ENV):
        return True
    key_file = Path(os.environ.get("MESOSPIM_AGENT_KEY_FILE", Path.home() / ".mesospim_agent_key"))
    if key_file.exists():
        # utf-8-sig: a key file written by PowerShell carries a BOM, which an HTTP header rejects.
        os.environ[config.KEY_ENV] = key_file.read_text(encoding="utf-8-sig").strip()
    return bool(os.environ.get(config.KEY_ENV))


@dataclass
class Turn:
    """One agent turn. `tools` is what the agent attempted, in order; `requests` is what the turn
    cost against the daily quota (one per model round-trip, so 1 + one per tool call)."""

    prompt: str
    tools: list = field(default_factory=list)
    reply: str = ""
    error: str = ""
    seconds: float = 0.0
    history: list = field(default_factory=list)

    @property
    def tool_names(self):
        return [name for name, _ in self.tools]

    @property
    def requests(self):
        return len(self.tools) + 1


def run_turn(acceptor, prompt, model=None, history=None):
    """Drive one turn through the production agent builder. Model/transport failures are captured
    on the Turn rather than raised: a scored run must survive a 429 on case 3 of 9 and report it."""
    from mesoSPIM.src.mesoSPIM_AiAssistent import build_agent

    cancel = threading.Event()
    turn = Turn(prompt=prompt)
    agent = build_agent(acceptor, cancel, on_call=lambda n, a: turn.tools.append((n, a)), model=model)
    started = time.monotonic()
    try:
        result = agent.run_sync(prompt, message_history=list(history or []))
        turn.reply = result.output
        turn.history = result.all_messages()
    except Exception as error:
        turn.error = f"{type(error).__name__}: {error}"
    turn.seconds = round(time.monotonic() - started, 2)
    return turn
