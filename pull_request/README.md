# Upstream PR — Remote Control for mesoSPIM-control

A proposed contribution to
[mesoSPIM-control](https://github.com/mesoSPIM/mesoSPIM-control): let an external
process control mesoSPIM over a socket by sending **named calls** — a **Remote
Control** tab that any external driver or script can build on. Nothing here is
driver-specific; it is a patch *for mesoSPIM*.

## The idea (why it is this small)

A client sends one single-key JSON object, `{"<method>": {args}}`. The server
**validates** it, looks the method up in a fixed allowlist (`COMMANDS`), and runs
the matching `mesoSPIM_Core` call — the same methods the GUI's own buttons call. It
returns a JSON result line. **No client Python is ever run**: the "method" is only
ever a dict-key lookup, so a client can only invoke operations the allowlist names.

An off-the-shelf **LLM** can drive the scope too: a small **MCP-over-HTTP** server
(a separate process) exposes `tools/list` *as* the allowlist and forwards a
`tools/call` to the same TCP command path — the same validated dispatch, just a
different envelope.

```
  a script (framed TCP)                                        INSIDE mesoSPIM
  {"move_absolute": {…}}  ─────────▶───────────┐              (Core context)
  __MESOSPIM_OK__{…}         ◀─────────◀──────────┤
                                               ├──▶  COMMANDS["move_absolute"](core, …)
  an LLM (MCP over HTTP)     ┌── forwards ──────┘        (one validated dispatch)
  POST /mcp {"tools/call"} ──┤  a framed TCP call
  {"result":{…}}           ◀─┘  (separate MCP process)
```

## What the PR does — the files

Three new self-contained modules. The two files mesoSPIM already owns gain **six lines between
them** — this is deliberate: the contribution should be easy to review, and easy to remove.

| File | Change |
|---|---|
| `mesoSPIM/src/mesoSPIM_RemoteControl_ValidateAndRunCommands.py` | **New.** The command vocabulary (`COMMANDS` allowlist, 55 commands), the arg gate `_validate` (type + allowed option + in-range), `run()` — the **single choke point** both transports share — `get_info`, bounded snapshot chunks, and `self_test()`, a fail-closed pre-flight smoke-check that the loaded limits really are enforced (against a `SimCore` mock of the hardware). Pure stdlib; unit-tested without Qt. Its module docstring states the four-step recipe for adding a command. |
| `mesoSPIM/src/mesoSPIM_RemoteControl_Servers.py` | **New.** A signal-driven `QTcpServer` (`RemoteControlTCPServer`) hosted by the Core that dispatches framed named calls and captures only a requested snapshot camera frame, **and** a standalone MCP-over-HTTP server (its own process, `--port` default `42100`) that forwards `tools/call` to that TCP server. **On start the TCP server runs `self_test` and refuses to bind if the limits aren't enforced**, so a drifted config never exposes the instrument. It also ships the **clients** — `RemoteControl` and `mcp_call` — so callers import the wire format rather than reimplementing it, and the MCP bridge uses the very same client an operator does. Constant-time token; HTTP adds a Bearer check and an Origin guard. |
| `mesoSPIM/src/mesoSPIM_RemoteControl_Tab.py` | **New.** `RemoteControlTab(QWidget)` — the Remote Control tab (TCP / MCP mode, host / port / password, default `smart_mesospim`). It owns its widgets, its settings, the MCP child process and its signals to Core, following the convention the project already uses for `mesoSPIM_Optimizer` and `ProcessorChainWindow`. It reflects the real start outcome, so a failed bind warns instead of showing a false "running". |
| `mesoSPIM/src/mesoSPIM_Core.py` | A declared `_remote_session` (the busy gate, operation counter and pending snapshot) and `_remote_control_server`; `start_remote_control(host, port, token)` / `stop_remote_control()` slots; and a `sig_remote_control_started(ok, message)` signal so a bind failure (e.g. port in use) is reported rather than assumed to have worked. Existing Core warning behavior is unchanged. |
| `mesoSPIM/src/mesoSPIM_MainWindow.py` | **Four lines**: the import, `self.remote_control = RemoteControlTab(self)`, and `self.remote_control.shutdown()` in `close_app()`. No remote-control state, widgets or methods live here. Existing warning-dialog behavior is unchanged. |
| `mesoSPIM/src/test_remote_control_validation.py` | Qt-free tests for the `_validate` gate. |
| `pyproject.toml` | A `mesospim-mcp-server` console entry point for the standalone MCP server. |

## Wire protocol

Length-framed UTF-8, both directions: `b"<decimal-byte-count>\n" + payload`. If a
token is set, the **first** frame must be it (`OK` / `AUTH-FAILED`). Every frame
after that is a call, `{"<method>": {args}}`; the reply is one `__MESOSPIM_OK__<json>`
line (or error text). MCP and direct TCP enter the same framing and validation path.
The full call format, every command's arguments, and the completion and safety rules are
in [`REMOTE_CONTROL_REFERENCE.md`](REMOTE_CONTROL_REFERENCE.md).

## Security

A named call **controls the microscope** (moves the stage, runs acquisitions), but
it is **not** arbitrary code — the server only ever does a dict lookup + a fixed
call, so nothing outside `COMMANDS` can run. And a bad *value* is refused too: the
args are **validated** (right shape, an option the live `cfg` allows, an in-range
number) before the Core is touched. Still:

- **Off by default**; started by an operator from the Remote Control tab.
- **Binds `127.0.0.1`** unless changed; the dialog warns before a network bind
  without a token.
- **Optional token** gates access (constant-time compare); over HTTP it is an
  `Authorization: Bearer <token>` header.
- **Origin guard (HTTP)**: any non-localhost `Origin` is rejected, so a web page in
  the operator's browser can't drive the instrument (DNS-rebinding / CSRF).
- **Plain TCP**: the token guards casual LAN access, **not** sniffer-proof. For
  untrusted networks, tunnel it (SSH/VPN) — out of scope for this PR.

## Input validation (`_validate`)

Before a call reaches the Core, `_validate` refuses a bad **value**, not just a bad
name — for **every** settable parameter, not only the stage — with a message the caller
can act on (and that **names the limit**, so a script or LLM can correct itself):

- **type** — a number where a number is expected (JSON booleans are *not* numbers), a
  string where a string is expected.
- **allowed option** — `filter`/`zoom`/`laser`/`shutterconfig` (and the same keys inside
  `set_state` / an acquisition) must be one the live `cfg` allows.
- **range** — `move_absolute` targets against the per-axis travel envelope of the config
  the operator **loaded at startup** (`cfg.stage_parameters`), so range checking is on by
  default with no extra setup; `MESOSPIM_RS_LIMITS` (a JSON object `{"x": [lo, hi], …}` or
  a path to one) can *tighten* an axis further. `intensity` and every `%` parameter ∈
  `[0, 100]`. No limit for an axis → the Core's hardware bound is the backstop.

Because both transports meet at the single `run()` choke point, **TCP and MCP can never
breach a limit** — an out-of-range call comes back as an error and never reaches the Core.
A client also has **no way to change the limits**: they come from the read-only `cfg` (plus
the env override) and no allowlisted verb writes them. `get_limits` returns the exact rules
in force — including which checks are **off** (`range: null` = only the type is checked) —
so a script or LLM can read the envelope up front.

## Info and snapshot pixels

This PR does not suppress or intercept mesoSPIM warning dialogs. They retain their existing
behavior for local and remote-triggered actions. The operation schema and `get_info` retain a
`warnings` list for future structured warnings, but existing GUI warnings are not captured in
that list.

`get_info` is the extensible microscope-information document. It currently reports the app,
version, protocol, state and stage type, current save and snap folders, last acquisition path,
ETL configuration path, and latest operation/warnings. It is rebuilt from current state on
every request and does not include snapshot metadata or pixels.

Remote `snap` never enters the GUI save/prefix path (`write: true` is rejected). It schedules
one camera snapshot, clients poll `get_progress` until that operation reports completion, and
then call `get_snap_image` with an optional operation ID, byte offset, and chunk size. Each
reply carries at most 512 KiB of raw pixels encoded as base64 plus `dtype`, `shape`, C-order,
total byte count, offsets, and SHA-256. Only the latest remotely requested snapshot is kept;
live-view and acquisition frames are never captured or exposed by this API.

## Pre-flight self-test (`self_test`)

The worry with any limits system is *drift*: a wrong limits file, a config quirk, a
validation regression — the limits silently stop matching this machine. So the server
**proves the limits before it goes live**. On **Start**, `RemoteControlTCPServer` runs
`self_test` first: it drives the whole validated `run()` dispatch against a **`SimCore`** that
carries *this instrument's real `cfg`* but simulates the hardware, and checks that a valid
move is accepted while an out-of-range move / bad option / unknown command is refused — and
that *only* the in-range moves reached the mock stage. If any check fails (including "no axis
has a limit"), it **raises and the server never binds** (fail-closed) — the instrument is
never exposed. It runs the *real* code both lanes share, so one gate covers TCP and MCP, and
it never touches real hardware. `self_test` is also an on-demand command on both lanes, so a
script or LLM can ask the server to re-prove its limits at any time.

## Tests

The short command manual is [TESTING.md](TESTING.md). Run every safe offline test with:

```powershell
python tests/run.py offline all
```

The opt-in `live valid` profile is excluded from normal CI. With an operator present and the
travel path clear, it moves X by a small in-range amount, verifies the change, restores X,
and then runs the complete valid-command DemoStage sweep:

```text
MESOSPIM_ALLOW_DEVICE_CHANGE=1 MESOSPIM_OPERATOR_PRESENT=1 \
MESOSPIM_LIVE_MCP_TOKEN=<token> MESOSPIM_TEST_X_DELTA_UM=100 \
MESOSPIM_LIVE_TCP_PORT=<port> MESOSPIM_LIVE_TCP_TOKEN=<token> \
python tests/run.py live valid mcp
```

Select `tcp` instead when the direct TCP server is active. The profile includes the small
move test and the complete valid-command DemoStage sweep.

The full visible demo sweep calls every one of the
55 allowlisted commands over the selected live transport (`mcp` by default, or `tcp`): all
40 instrument-facing operations and 15 read/query commands.
It refuses to run unless the server reports `DemoStage`, uses a temporary acquisition
directory, backs up and restores the ETL CSV, and restores position, settings, acquisition
list, shutters, and idle state. On Windows it completes in about 50 seconds:

Set `MESOSPIM_CONFIRM_DEMO_MODE=1`, `MESOSPIM_RUN_ALL_COMMANDS=1`,
`MESOSPIM_DEMO_PROCESS_ID`, `MESOSPIM_DEMO_ROOT`, and
`MESOSPIM_DEMO_ETL_CONFIG_PATH` before using the `live valid` profile.

For the identical framed-TCP sweep, run `python tests/run.py live valid tcp` and set
`MESOSPIM_LIVE_TCP_PORT` plus `MESOSPIM_LIVE_TCP_TOKEN`.

Without every gate, the sweep safely skips. It must never be enabled against physical
hardware; the remote `DemoStage` check is an additional fail-closed guard. The full sweep
runs at most once per transport in each mesoSPIM process, allowing an MCP-then-TCP sequence
to expose lifecycle defects without creating an unbounded loop. The group also contains a live
cross-transport operation-gate check: MCP starts a short demo acquisition, a simultaneous
valid TCP mutation must receive the active command and operation ID as a busy error, status
polling remains available, and the next TCP mutation is accepted after real completion.

The `live adversarial` profile is demo-only. It first verifies
the remote stage is exactly `DemoStage`, then sends hostile, malformed, out-of-range, and
state-machine-smuggling attacks through the selected lane (`mcp`, `tcp`, or `both`). It
proves state is unchanged and the selected lanes recover before running a simultaneous
24-call busy-gate burst (16 mutations, 8 reads). This group is intentionally destructive
in intent and must never run on hardware:

```text
MESOSPIM_ALLOW_DEVICE_CHANGE=1 MESOSPIM_OPERATOR_PRESENT=1 \
MESOSPIM_CONFIRM_DEMO_MODE=1 MESOSPIM_RUN_LIVE_ADVERSARIAL=1 \
MESOSPIM_LIVE_MCP_TOKEN=<token> \
MESOSPIM_LIVE_TCP_PORT=<internal-port> MESOSPIM_LIVE_TCP_TOKEN=<internal-token> \
MESOSPIM_DEMO_PROCESS_ID=<pid-of-mesoSPIM-Control--D> \
MESOSPIM_DEMO_ROOT=<path-to-mesoSPIM> \
MESOSPIM_DEMO_ETL_CONFIG_PATH=<path-to-ETL-parameters.csv> \
python tests/run.py live adversarial both
```

For a single-lane run, select `tcp` or `mcp` and provide only that lane's connection
settings. The current corpus contains 55 MCP attacks and 56 TCP attacks (111 in the
combined run, including lane-specific malformed framing probes).

The API currently exposes 55 commands, 15 explicitly ranged parameters, four config-driven
enums, and 21 type-only parameters. The offline adversarial group crosses both sides of all
15 configured ranges through the dedicated setter and `set_state`, and also covers
acquisition-list/acquire-start ranges, enums and stage coordinates, row/index values, and
time-lapse arguments. The bounded live corpus crosses every configured absolute and
relative stage bound plus representative acquisition, camera, ETL, galvo, laser-timing,
row/index, and time-lapse boundaries over the selected real transport lanes.

`tests/unit/test_validation.py` rebuilds both modules straight
from the `0001-*.patch` new-file hunks and checks the promises **without Qt**:
framing round-trips, the token is constant-time, bad **values** are refused
(type / option / range for every settable, not just the stage), the limits come from
`cfg.stage_parameters` end to end, both lanes refuse an out-of-limit call with an error,
and the MCP reply shape is right. `tests/unit/test_adversarial.py` is a
**wide** sweep that tries to break the two guarantees: ~20 hostile method names
(dunders, dotted paths, Python expressions, unicode/whitespace/NUL variants) that must
never run, every malformed envelope shape, every axis breached in both directions,
`NaN`/`inf`/huge numbers, type confusion in every value slot, attempts to *change* the
limits, MCP hostile `tools/call` names turned into `isError` JSON, and framing/auth
tricks — all against a `_RecordingCore` so each refusal is proven to leave the Core
**untouched**. `tests/integration/test_viability.py` stands up the real server on both lanes
and runs the operator's viability check; the unit suite also proves the start-time
self-test **gate** (a config with no limits makes construction raise before it ever binds).
`tests/integration/test_transport_security.py` adds a **black-box transport sweep**: every attack
enters through a real loopback MCP/HTTP request or framed TCP socket, MCP forwards through
TCP exactly as it does in production, and a recording fake Core proves rejected inputs never
reach instrument methods. It covers duplicate auth/origin headers and JSON members,
non-finite numbers, relative-move limit crossings, oversized MCP bodies/TCP frames,
Unicode/delimiter fuzz, malformed JSON-RPC, auth/origin bypass strings, pipelining, and
post-attack liveness.
`tests/integration/test_valid_transports.py` is the matching positive black-box contract matrix:
one representative, usable request for every one of the 55 allowlisted commands, sent over
both real loopback MCP/HTTP and framed TCP paths (110 transport cases), plus a completeness
check that fails when the allowlist and test table drift apart. It also verifies the Core
call, state change, or returned value expected from each command.
`tests/live/test_all_commands.py` is the corresponding real-Core demo sweep. It logs
each command, verifies observable readback where available, continues after a failure so one
run identifies every broken command, and restores demo state in a `finally` block.
`tests/live/test_adversarial.py` is the fail-closed `DemoStage`-only attack suite:
it sends the real hostile API corpus and mixed MCP/TCP concurrency burst, verifies no state
leaks through rejected calls, records observed response latency, and checks recovery.
The shipped `test_remote_control_validation.py` covers the same `_validate` gate against the
real module. The complete offline suite is **225 passing tests in under 10 seconds** on the
Windows test environment. The offline/adversarial tests are deliberately bounded: no
`sleep`, no unbounded fuzz or retries, at most 48 seeded fuzz mutations, a maximum of 56
live attacks per lane, and a 0.6-second deadline on every offline test socket.

**Bench verification in mesoSPIM `-D` demo mode (2026-07-14, after the structural cleanup):**
the patch was applied to official `release/candidate-py312` commit
`b3c9638acb9c15394d4e371cb44e916a2f2e9664` and mesoSPIM was launched in demo mode from the
applied tree. The Remote Control tab appeared in its expected position after Timelapse. Both
transports were driven end to end from the GUI, one after the other.

| Check | TCP | MCP |
|---|---|---|
| Fail-closed `self_test` passes and the server binds | yes | yes |
| All **55** commands functional, safe, state restored | yes (51.2 s) | yes (60.8 s) |
| Hostile-input corpus rejected, then recovers | yes | yes |
| Busy gate survives bounded mixed concurrency | yes | yes |
| Snapshot reconstructed and its SHA-256 verified | yes | yes |

Every `tools/call` over MCP is forwarded through the same `RemoteControl` client an operator
imports, so the MCP column above is also 55 round-trips through that client.

Starting **MCP** stopped the public TCP server, bound an internal TCP server on an ephemeral
port, and launched the MCP bridge as a separate process holding a freshly generated internal
token — the operator's password guards only the public MCP endpoint. **Closing mesoSPIM with
MCP running left no orphan child process and no listening port.**

The live adversarial suites are `DemoStage`-only and enforce it at run time, not by trust: each
one calls `get_limits` over the live connection and **fails** (it does not skip) if the reported
`stage_type` is not `DemoStage`. Pointing them at an instrument refuses rather than proceeds.

The offline suite is **225 passing tests**, green both when loaded from this patch file and when
loaded from the source tree.

**Retained warning, not reproduced here:** in the 2026-07-13 campaign on the pre-cleanup code,
one MCP run — performed after an additional standalone snapshot in the same process — ended
during acquisition camera/writer teardown with native Windows heap-corruption code
`0xc0000374` in `ntdll.dll` and no Python exception; an unchanged fresh process then passed the
complete sweep. It did not recur in the runs above, but it is recorded rather than quietly
dropped. Existing mesoSPIM warning dialogs remained enabled throughout. **Real-hardware**
testing is intentionally out of scope for this contribution.

## How to apply

Cut against **`release/candidate-py312`**, so from that branch it applies cleanly:

```bash
git checkout release/candidate-py312
git checkout -b remote-control
git am 0001-Add-optional-Remote-Control-tab-TCP-MCP-named-call-s.patch
```

(If your tip has drifted, use `git am --3way`.) Then launch mesoSPIM and use the
**Remote Control** tab → **Start**.

## How a driver builds on it

An external driver is *one client* of this bridge, and it does not have to implement the wire
format — the client ships with the server:

```python
from mesoSPIM.src.mesoSPIM_RemoteControl_Servers import RemoteControl, mcp_call

scope = RemoteControl(port=42000, token="...")
scope.call("move_absolute", targets={"x": 100})
```

A driver's command vocabulary lives on the client side as a mirror of `COMMANDS`; mesoSPIM
learns no client concepts.

---
Author: Thom de Hoog (ZMB, University of Zurich) · thom.dehoog@zmb.uzh.ch ·
thomdehoog@gmail.com. Patch license: **GPL-3.0** (part of mesoSPIM-control).
