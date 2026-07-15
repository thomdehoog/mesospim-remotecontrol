# Upstream contribution: Remote Control for mesoSPIM

This patch adds an optional Remote Control tab to mesoSPIM-control. An external program can use the
same 53 named commands through framed TCP or a small MCP-compatible HTTP endpoint.

The feature is off by default. An operator chooses one transport, enters its host, port, and
password, and clicks Start. TCP and MCP cannot run together.

## Main behavior

A client sends a command name and JSON arguments. For example:

```json
{"move_absolute":{"targets":{"x":100}}}
```

The server looks up that exact name in a fixed command list. It validates the arguments and active
limits before calling mesoSPIM. It never executes client Python or free-form text.

TCP and MCP share the same command implementation, so their accepted values, rejected values,
operation state, and limits cannot drift apart.

## Asynchronous operations

Every ordinary mutation uses the same asynchronous contract. mesoSPIM validates the full request,
checks hardware limits and the one-mutation gate, reserves an operation ID, and returns
`accepted: true` with `status: processing`. The command runs on the next Qt event-loop turn. Clients
poll `get_progress`, match the operation ID, and read either the completed `result` or failed
`error`.

Read-only commands still return current data directly. Emergency stop and shutter commands remain
immediate so they are available while another operation owns the gate.

Stage moves now follow this sequence:

1. Validate and accept the target.
2. Return `processing` with an operation ID.
3. Issue the move with `wait_until_done=False`.
4. Check normal mesoSPIM position readback.
5. Report `completed` only after the requested axes reach their targets.

This keeps the initial TCP or MCP reply independent of hardware and GUI duration. Clients must not
repeat any accepted mutation while waiting for completion.

## Patch contents

Five new modules contain the feature:

| File | Responsibility |
| --- | --- |
| `mesoSPIM_RemoteControl_Config.py` | Constants, ports, protocol values, axes, and tolerances |
| `mesoSPIM_RemoteControl_Dispatcher.py` | Operation state, one-mutation rule, replies, strict JSON, and error codes |
| `mesoSPIM_RemoteControl_Commands.py` | Validation, limits, startup self-test, and all 53 commands |
| `mesoSPIM_RemoteControl_Servers.py` | Core-thread routing, framed TCP, MCP HTTP, startup, and shutdown |
| `mesoSPIM_RemoteControl_GUI.py` | Operator controls and the acquisition-table bridge |

Two existing files receive only integration hooks:

- `mesoSPIM_Core.py`: one signal, two attributes, and two small start/stop methods.
- `mesoSPIM_MainWindow.py`: import the tab, create it, and close it during shutdown.

No command or network logic is added to Core or MainWindow. The exact additions are shown in
[`../impl/INTEGRATION.md`](../impl/INTEGRATION.md).

## Connection summary

TCP defaults to `127.0.0.1:42000`. Each message is a UTF-8 payload preceded by its byte count and a
newline. The password is the first frame.

The MCP-compatible endpoint defaults to `http://127.0.0.1:42100/mcp`, uses a Bearer password,
advertises revision `2024-11-05`, and provides `initialize`, `tools/list`, and `tools/call`. Tool
calls are sent to the same command path as TCP calls. The endpoint is intentionally not a complete
implementation of newer MCP Streamable HTTP revisions.

The complete format and command table are in
[`REMOTE_CONTROL_REFERENCE.md`](REMOTE_CONTROL_REFERENCE.md).

## Validation and limits

Before a command reaches mesoSPIM, the server checks:

- command and argument names;
- JSON types, including rejecting booleans where numbers are required;
- finite numeric values;
- configured filter, zoom, laser, shutter, and camera options;
- fixed ranges for intensity, timing, ETL, galvo, and camera values;
- absolute and relative stage targets against every configured axis limit;
- the shared one-mutation-at-a-time operation state.

`get_limits` reports the rules currently in force. `MESOSPIM_RS_LIMITS` may tighten stage limits but
cannot widen the loaded configuration.

Startup runs `self_test` against a simulated Core carrying the loaded configuration. It proves that
valid moves pass and out-of-limit moves fail without touching hardware. Missing or invalid limits
prevent the network listener from starting.

## Safety and security

- Remote Control starts only after an operator clicks Start.
- Exactly one transport is active.
- A password is required.
- The repository's default password is refused outside loopback.
- MCP rejects non-local browser origins.
- Incoming frames and HTTP bodies have size limits.
- A second remote mutation is rejected while one is active.
- Reads and emergency commands remain available during asynchronous stage movement.
- Application shutdown waits for the listener to close.

TCP and HTTP are not encrypted. Use a trusted machine, SSH tunnel, or VPN outside loopback.

An authenticated client is a trusted microscope operator. File-related calls can inspect supplied
paths, reload an ETL file, and save the active ETL configuration; they are not a filesystem sandbox.
Keep the service on loopback or a protected network and do not share its password.

The remote operation rule does not block the local GUI. The operator must avoid conflicting local
actions while a remote operation is active.

## Acquisition behavior

Remote acquisition rows are validated before installation. The same list object is placed in Core
state and the visible acquisition table so time-lapse code cannot restore stale GUI rows.

`acquire_start` saves the operator's current list, installs one temporary row, and starts it.
`acquire_finish` restores the original list exactly, including its `planes` metadata.

The API reports state, metadata, file paths, and progress. It does not transmit image pixels.

## Tests

Install the pinned development tools, then run both offline modes:

```bash
pip install -r pull_request/requirements-dev.txt
python pull_request/tests/run.py offline all
MESOSPIM_RC_SOURCE_ROOT="$PWD/impl" python pull_request/tests/run.py offline all
```

With PyQt5 installed:

```bash
python pull_request/tests/real_pyqt_smoke.py
python pull_request/tests/real_pyqt_transport_smoke.py
```

The second smoke test uses temporary loopback TCP and MCP ports with a fake Core. It verifies prompt
acceptance for both short actions and movement, responsive polling, and readback-based completion.
It does not start mesoSPIM or hardware.

Live Windows tests require an operator and DemoStage. The suite never starts or stops a transport;
the operator does so manually. See [`TESTING.md`](TESTING.md).

## Current verification status

- Main implementation suite: 341 passed.
- Pull-request suite from source: 211 passed, with nine opt-in live tests skipped.
- Pull-request suite from the generated patch: 211 passed, with nine opt-in live tests skipped.
- Ruff: passed.
- Dead-code scan at high confidence: passed.
- Real PyQt widget/shutdown smoke: passed.
- Real PyQt TCP and MCP asynchronous action and movement smoke: passed.
- Patch applies cleanly to upstream `b3c9638`, the tested `d16546c` integration base, and PR #105's
  current base `560dcf0`.
- The five generated modules are byte-identical to `impl/`.
- Existing Core/MainWindow patch size remains 15 additions and four additions, respectively.

The unified-model Windows build is fully green across both transports. TCP passed its targeted
native-list regression, complete valid suite, and all six adversarial groups: 9 passed, 0 failed,
0 skipped. MCP passed its complete valid suite and all six adversarial groups: 8 passed, 0 failed,
0 skipped. All 53 commands, admission and busy races, acquisition recovery, native-list restoration,
time-lapse recovery, state restoration, and artifact cleanup passed. Only the final normal
File > Exit process/port/worker check remains unreported. See
[`../REVIEW_REPORT.md`](../REVIEW_REPORT.md).
