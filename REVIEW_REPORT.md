# Remote Control review and release handoff

## Current decision

Status: **implementation and live transport gates passed; final GUI teardown confirmation pending**.

The source, generated patch, offline tests, real-PyQt transport tests, and full Windows DemoStage
valid and adversarial suites are green. MCP and TCP both verified the asynchronous stage-movement
fix. The remaining release check is to close the final asynchronous DemoStage build through
File > Exit and confirm that its process, both ports, and worker threads exit normally.

Upstream patch base: `mesoSPIM/mesoSPIM-control@b3c9638acb9c15394d4e371cb44e916a2f2e9664`

The patch also applies cleanly to the newer Windows integration base `d16546c`.

## Defect found during Windows testing

A fresh full MCP test exposed a real defect:

1. `move_absolute({"x": 100})` passed validation.
2. DemoStage moved from X=0 to X=100.
3. The MCP HTTP request did not receive a response before the client timeout.
4. Cleanup requests also timed out, so X remained at 100.
5. The MCP port stayed open, but later HTTP calls received no response until MCP was restarted.

The Windows console confirmed both sides of the failure. It printed `INFO: x_pos = 100.0`, followed
by `ConnectionAbortedError: [WinError 10053]` while the server tried to write responses after the
clients had already timed out.

This was not an authentication, limit, or movement failure. The command reached Core and moved the
stage, but `wait_until_done=True` kept the Core-thread request open too long. The network client gave
up before the reply arrived.

## Implemented and verified fix

All stage-position commands now use one asynchronous contract:

- `move_absolute`
- `move_relative`
- `load_sample`
- `unload_sample`
- `center_sample`

The new sequence is:

1. Validate the request and reserve the one-operation gate.
2. Return `accepted: true`, an operation ID, `status: processing`, and the target.
3. Issue the stage command with `wait_until_done=False`.
4. Check mesoSPIM's normal position readback on a short Qt timer.
5. Mark the operation `completed` only when every requested axis is within tolerance.
6. Mark it `failed` if it is stopped before reaching the target or if issuing the move raises.

`get_progress` exposes the requested `target` and latest `observed` position. Clients must keep the
operation ID, poll through the same TCP or MCP transport, and never repeat an accepted move simply
because it is slow.

The recovery command cannot clear an unconfirmed stage move just because the general Core state is
`idle`. The operator must stop it first.

The HTTP response writer now quietly handles Windows client-abort errors. Server dispatch timeouts
are logged with the latest operation, and live-client timeouts must be longer than the server's own
dispatch timeout.

## Integration footprint

The fix is entirely inside the Remote Control modules and tests. It adds no Core or MainWindow code.

The upstream patch still changes existing files by only:

| Existing file | Patch size |
| --- | --- |
| `mesoSPIM_Core.py` | 15 added lines |
| `mesoSPIM_MainWindow.py` | 4 added lines |

All command, polling, transport, and validation logic remains in the five new
`mesoSPIM_RemoteControl_*` modules.

## Offline and real-PyQt verification

| Check | Result |
| --- | --- |
| Main implementation suite | 339 passed |
| Pull-request suite, source mode | 209 passed |
| Pull-request suite, patch mode | 209 passed |
| Ruff | passed |
| High-confidence dead-code scan | passed |
| Real PyQt widget and shutdown smoke | passed |
| Real PyQt MCP asynchronous stage smoke | passed |
| Real PyQt TCP asynchronous stage smoke | passed |
| Patch apply on `b3c9638` | passed |
| Patch apply on `d16546c` | passed |
| Five generated modules vs `impl/` | byte-identical |
| `git diff --check` | passed |

The new real-PyQt transport test uses a fake Core and temporary loopback ports. For both TCP and MCP
it proves that:

- the move reply returns `processing` before the target is reached;
- a second independent connection can read progress while movement is active;
- polling reaches `completed` only after position readback reaches X=100;
- the move is issued once, with `wait_until_done=False`.

It does not start mesoSPIM or access hardware.

## Windows DemoStage release results

The asynchronous implementation was applied to a fresh upstream `d16546c` integration build. The
operator started and stopped each transport manually, and MCP and TCP were never active together.

MCP results:

- Safe X regression passed in 0.50 seconds and restored X from 0 to 100 to 0.
- The complete valid suite passed all 53 commands and 37 operational calls.
- All six adversarial groups passed with no failures or skips.
- The hostile corpus rejected 210 attacks without changing protected state.
- Busy stress rejected 16 competing mutations while serving eight reads.
- Acquisition lifecycle, five preflight refusals, exact acquisition-list restoration, and the
  time-lapse idle period passed.

TCP results:

- The complete valid suite passed: 2 tests in 48.98 seconds.
- The complete adversarial suite passed: 6 tests in 17.49 seconds.
- The hostile corpus rejected 211 attacks without changing protected state.
- Busy stress rejected 16 competing mutations while serving eight reads.
- Acquisition lifecycle, five preflight refusals, exact acquisition-list restoration, and the
  time-lapse idle period passed.

Final restoration checks reported Core idle, position restored, shutters closed, settings and the
native acquisition row restored, no ETL Git diff, and no known test artifacts. Each transport
remained healthy until the operator stopped it, and the other transport remained closed.

After these live gates, the final module names, documentation, formatting, and hardware-free
self-test cleanup were completed. Those changes pass the complete offline and real-PyQt suites and
do not change live command behavior.

## Remaining release action

Close the final asynchronous DemoStage build with **File > Exit**. Verify the main process exits,
ports 42000 and 42100 close, and no mesoSPIM or Qt worker remains. Do not force termination unless
normal teardown fails and the operator explicitly approves it.

## What the adversarial test must cover

The live adversarial suite is intentionally broader than malformed JSON. It checks:

- correct and incorrect command names;
- missing, extra, wrong-type, duplicate, non-finite, and out-of-range values;
- stage and hardware-setting limits;
- authentication, frame sizes, HTTP paths, origins, and truncated requests;
- accepted and rejected calls over the selected transport;
- simultaneous mutations, reads during a busy operation, and emergency commands;
- one active transport only;
- acquisition start, progress, completion, cleanup, and file ownership;
- disk, overwrite, geometry, path, and other preflight failures;
- time-lapse idle periods that must not be mistaken for a stuck operation;
- state, acquisition-list, ETL, shutter, position, and file restoration;
- transport health after every failure group;
- clean application shutdown.

## Remaining limits

1. The remote one-operation rule does not prevent a local GUI action from conflicting with a remote
   operation. Operator procedure must prevent that race.
2. `get_progress` stores only the latest operation. Clients must keep and compare its ID.
3. MCP implements the small method set required here, not the complete MCP specification.
4. TCP and HTTP are unencrypted. Use loopback, SSH, or a VPN on an untrusted network.
5. Real warning dialogs remain operator-visible; tests do not dismiss them automatically.
6. Physical microscope commissioning is still required after DemoStage passes. Start with small,
   reversible moves and verify axis direction, units, limits, stop behavior, and readback.
7. Authenticated file-related commands run with mesoSPIM's host access. They are trusted-operator
   tools, not a filesystem sandbox.

The only recurring dependency warning is the upstream `numcodecs` CRC32C deprecation. It does not
change Remote Control behavior.

## Release criterion

The full MCP and TCP valid/adversarial gates and restoration checks have passed. Mark the release
gate fully green after the remaining File > Exit check confirms clean process, port, and worker
shutdown.
