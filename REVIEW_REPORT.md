# Remote Control review and release handoff

## Current decision

Status: **the unified asynchronous command model passes all hardware-free, TCP DemoStage, MCP
DemoStage, state-restoration, cleanup, and normal-shutdown gates**.

The generated upstream patch is based on
`mesoSPIM/mesoSPIM-control@b3c9638acb9c15394d4e371cb44e916a2f2e9664`. It also applies cleanly to
the tested integration base `d16546c` and PR #105's current base `560dcf0`.

Release testing is complete. Upstream pull request #105 remains unchanged and should be updated only
after the repository owner gives explicit approval.

## Why the command model changed

Windows testing found two cases where mesoSPIM completed a command but the client timed out waiting
for its original response:

1. An MCP stage move reached X=100, but the request remained open while movement completion was
   being handled.
2. A TCP `set_acquisition_list` restoration completed and restored the native `planes=10` row, but
   the client timed out while the command waited for GUI-table synchronization.

These were response-delivery failures, not rejected commands or failed microscope actions. The old
hybrid model made stage movement asynchronous but still executed 19 short mutations before sending
their replies. A slow Core or GUI call could therefore make a successful command look unsuccessful.

## Implemented command contract

All ordinary mutations now use one sequence over both TCP and MCP:

1. Authenticate the connection.
2. Check the command name and argument object.
3. Validate types, configured options, hardware parameters, stage targets, and all active limits.
4. Atomically reject the call as `busy` if another mutation is `processing` or `stopping`.
5. Reserve the one-mutation gate and create an operation ID.
6. Return `accepted: true` with `status: processing`.
7. Run the command on the next Qt event-loop turn.
8. Poll `get_progress` for the same operation ID until it reports `completed` or `failed`.

Nothing executes when validation or the busy check rejects a call. Accepted commands are never
silently retried. Their command-specific output is stored in `operation.result`; execution failures
are stored in `operation.error`.

The four command categories remain useful and explicit:

- `READ` returns current data directly and does not open the mutation gate.
- `ACTION` is admitted asynchronously and completes when its scheduled function returns.
- `WAIT` is admitted asynchronously and completes only at a verified milestone.
- `EMERGENCY` executes immediately so stopping and shutter closure remain available while busy.

This design makes the initial reply independent of stage duration, acquisition preflight, ETL work,
or the blocking Core-to-GUI acquisition-list bridge. The gate stays closed until the scheduled work
actually finishes, so another mutation cannot race it.

## Stage movement

Absolute, relative, load, unload, and center moves additionally use this completion rule:

1. Issue the move with `wait_until_done=False`.
2. Poll mesoSPIM's normal position readback on a short Qt timer.
3. Report `completed` only when every requested axis is within its configured tolerance.
4. Report `failed` if the move is stopped before the target is reached or issuing it raises.

`get_progress` exposes the requested `target`, latest `observed` position, and terminal `result`.
Recovery cannot clear an unconfirmed target merely because Core's general state says `idle`.

## TCP connection cleanup

An earlier adversarial preflight run also found a stale Qt socket callback:

```text
RuntimeError: wrapped C/C++ object of type QTcpSocket has been deleted
```

The TCP adapter now drops a client when a queued read or write finds that Qt has already deleted its
socket. Offline tests cover both paths. The focused Windows preflight regression subsequently passed
all five refusal cases with reconnect-per-call polling.

## Integration footprint

All scheduling, polling, validation, transport, and command logic remains in the five new
`mesoSPIM_RemoteControl_*` modules. The existing-file footprint remains:

| Existing file | Added lines |
| --- | ---: |
| `mesoSPIM_Core.py` | 15 |
| `mesoSPIM_MainWindow.py` | 4 |

The acquisition-list GUI bridge still installs the same validated object in Core and the visible
table. It now runs after acceptance, and clients poll its operation before issuing another mutation.
No additional Core or MainWindow code was needed for the new command contract.

## Simplicity and overengineering screen

The production path was screened for unnecessary dependencies, abstractions, hard-coded values,
duplicate logic, dead code, and speculative compatibility work. The result is acceptable for the
upstream pull request:

- MCP uses Python's standard `http.server`, `json`, `hmac`, `logging`, and `threading` modules. It
  does not introduce Flask, FastAPI, an ASGI server, a web framework, or a separate process.
- TCP uses Qt's existing `QTcpServer` because its connections and command admission must remain on
  mesoSPIM's Qt/Core event loop. Adding another socket framework would increase threading and
  shutdown complexity.
- PyQt5 is the only non-standard-library runtime dependency, and mesoSPIM already requires it.
- The dispatcher contains the shared registry, operation state, admission lock, scheduling, strict
  JSON handling, and typed errors. Normal command definitions, arguments, hardware limits, and Core
  calls remain in `_Commands.py`.
- Defaults, protocol constants, timeouts, shared hardware ranges, and operation milestones remain
  in `_Config.py`. Protocol-defined JSON-RPC codes and local GUI layout measurements stay near their
  single use rather than becoming unnecessary global constants.
- `_GUI.py` builds one small tab and one acquisition-table bridge. It does not introduce a general
  plugin framework, settings framework, or new MainWindow abstraction.
- The 53-command registry is intentionally explicit. Small validators and execution functions are
  easier to review than generated handlers, inheritance trees, or command-specific server branches.
- The high-confidence dead-code scan reports no unused production code. Ruff, formatting, spelling,
  syntax, and the full hardware-free suites also pass.

The following compatibility guards are retained because they protect observed behavior rather than
hypothetical platforms:

- Qt thread marshalling prevents network threads from calling Core or hardware directly.
- Deleted-`QTcpSocket` handling covers the exact stale-callback failure observed during the Windows
  adversarial run.
- Lost-client response handling covers Windows `ConnectionAbortedError` and equivalent socket
  disconnects without changing command execution.
- Acquisition preflight reconciliation handles upstream refusal paths that emit completion while
  leaving the Core state unchanged.
- Gate recovery recognizes `time_lapse_start` because Core is legitimately `idle` between time
  points. This narrow safety guard prevents recovery from clearing an active time lapse; it does not
  execute or validate the command.
- Native acquisition metadata preserves upstream's real `planes=10` row even when its Z geometry
  derives 11 images.
- The blocking Core-to-GUI acquisition-list bridge is required because the visible Qt model and
  Core must own the same list before the operation is marked complete.

No runtime code was changed merely to reduce line count or satisfy a complexity score. The few
higher-complexity functions are bounded protocol or validation decision trees; splitting them would
add indirection without removing behavior.

## Hardware-free verification

| Check | Result |
| --- | --- |
| Main implementation suite | 341 passed |
| Pull-request suite, source mode | 211 passed, 9 live tests skipped |
| Pull-request suite, generated-patch mode | 211 passed, 9 live tests skipped |
| Ruff | passed |
| Real PyQt widget and shutdown smoke | passed |
| Real PyQt MCP asynchronous action and movement smoke | passed |
| Real PyQt TCP asynchronous action and movement smoke | passed |
| Patch apply on `b3c9638` | passed |
| Patch apply on `d16546c` | passed |
| Patch apply on current PR base `560dcf0` | passed |
| `git diff --check` | passed |

The real-PyQt transport test uses a fake Core and temporary loopback ports. For both transports it
proves that:

- a short action returns `processing` before its Core method runs;
- a move returns `processing` before its target is reached;
- a separate progress call remains usable;
- polling returns the terminal command result;
- movement completes only after readback reaches X=100;
- the move is issued once with `wait_until_done=False`.

It does not start mesoSPIM or access hardware.

## Windows DemoStage results

The final product build `fecc74f` passed both operator-controlled transports. MCP and TCP were
tested separately.

TCP:

- complete valid suite: 2 passed;
- complete adversarial suite: 6 passed;
- total: 8 passed, 0 failed, 0 skipped.

MCP:

- complete valid suite: 2 passed;
- complete adversarial suite: 6 passed;
- total: 8 passed, 0 failed, 0 skipped.

Together these gates verified:

- all 53 commands and 37 operational calls;
- 210 rejected hostile MCP requests and 211 rejected hostile TCP requests;
- exactly one accepted mutation in the 10-call free-gate race;
- 16 rejected mutations and eight successful reads during busy stress;
- acquisition lifecycle and five reconnect-per-call preflight refusals;
- exact restoration of the native acquisition row with `planes=10`;
- time-lapse idle-gap recovery;
- Core idle, exact position restoration, settings restoration, unchanged ETL data, and no known test
  artifacts;
- healthy transport operation after the full suites.

The only runtime warning was the upstream `numcodecs` CRC32C deprecation.

## Final teardown result

The operator stopped TCP and closed mesoSPIM with **File > Exit**. Process 58744 exited normally,
ports 42000 and 42100 closed, and no mesoSPIM, Python, or Qt worker remained. No forced termination
was needed.

## Adversarial coverage requirement

The release gate must cover more than malformed JSON:

- correct, misspelled, unknown, and forbidden command names;
- missing, extra, duplicate, wrong-type, non-finite, and out-of-range values;
- every reported stage and hardware-parameter limit;
- authentication, origins, HTTP paths, frame limits, and truncated requests;
- a 10-call free-gate admission race with exactly one accepted mutation;
- a busy-gate race with 16 rejected mutations and eight successful reads;
- reads and emergency commands while an operation owns the gate;
- exact accepted/rejected behavior over both transports;
- acquisition lifecycle, preflight refusals, files, and cleanup;
- native acquisition-list and `planes` metadata restoration;
- time-lapse idle intervals that must not be mistaken for a wedge;
- transport health after failures;
- clean application shutdown.

## Remaining limits

1. The one-operation rule coordinates remote clients, not local GUI actions. The operator must avoid
   conflicting local actions while a remote operation is active.
2. `get_progress` stores only the latest operation. Clients must retain and compare its ID.
3. A long synchronous Core action can temporarily delay polling, especially on the Qt TCP listener.
   Retrying a read-only progress call is safe; resending an accepted mutation is not.
4. MCP implements the small method set required here, not the complete MCP specification.
5. TCP and HTTP are unencrypted. Use loopback, SSH, or a VPN on an untrusted network.
6. Warning dialogs remain visible to the operator; tests do not dismiss them automatically.
7. Authenticated file commands use mesoSPIM's host access. They are trusted-operator tools, not a
   filesystem sandbox.
8. Physical microscope commissioning is still required after DemoStage passes. Begin with small,
   reversible moves and verify direction, units, limits, stop behavior, and readback.

The recurring `numcodecs` CRC32C deprecation is an upstream dependency warning and does not change
Remote Control behavior.

## Release criterion

The complete MCP and TCP valid/adversarial gates and the final normal File > Exit teardown are green.
The contribution is ready for upstream review when the repository owner approves updating pull
request #105.
