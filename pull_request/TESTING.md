# Running the tests

## 1. Main unit suite

Start with the hardware-free tests:

```bash
cd impl
python -m pytest tests -q
```

They test all 53 commands, valid and invalid input, limits, operation state, simultaneous calls,
TCP framing, MCP HTTP behavior, and shutdown. A small PyQt replacement is used, so no microscope or
display is needed.

## 2. Test the source and the generated patch

The tests under `pull_request/tests/` can load code from two places:

- **Patch mode** is the default. It extracts the five modules from `0001-*.patch`.
- **Source mode** uses the directory named by `MESOSPIM_RC_SOURCE_ROOT`.

Run both after changing code:

```bash
python pull_request/tests/run.py offline all
MESOSPIM_RC_SOURCE_ROOT="$PWD/impl" python pull_request/tests/run.py offline all
```

Each run prints its source. Check that line. If a source-mode change is accidentally tested in patch
mode, an old patch can appear green. Regenerate the patch and rerun both modes before publishing.

The test state deliberately supports only methods available on the real
`mesoSPIM_StateSingleton`. Do not add dictionary conveniences such as `.get()` merely to make a
test pass; production state does not provide them.

## 3. Real PyQt smoke tests

With PyQt5 installed:

```bash
python pull_request/tests/real_pyqt_smoke.py
python pull_request/tests/real_pyqt_transport_smoke.py
```

The first script checks real widgets, Qt signals, timers, and shutdown without opening a port. The
second opens temporary loopback TCP and MCP ports against a fake Core. It proves that:

- a stage call returns `processing` before the target is reached;
- a second connection can read progress while movement is active;
- completion appears only after position readback reaches the target.

Neither script starts mesoSPIM or accesses hardware.

## 4. Windows DemoStage tests

Live tests never start or stop MCP or TCP. The operator must do that in the Remote Control tab.
Only one transport may run at a time.

Available profiles:

```powershell
python pull_request\tests\run.py live valid mcp
python pull_request\tests\run.py live adversarial mcp
python pull_request\tests\run.py live valid tcp
python pull_request\tests\run.py live adversarial tcp
```

Required safety settings are read by `tests/support/live_session.py`. They include confirmation that
an operator is present, device changes are allowed, and the reported stage is `DemoStage`. Passwords
and transport addresses are supplied through `MESOSPIM_LIVE_MCP_*` or `MESOSPIM_LIVE_TCP_*`
environment variables; do not store them in the repository.

For each transport:

1. Start it manually.
2. Confirm the other port is closed and `get_limits` reports `DemoStage`.
3. Run the valid suite.
4. Run the adversarial suite only if the valid suite passes and state was restored.
5. Stop the transport manually before switching.

The live suites check movement and restoration, all commands, hostile inputs, limits, a 10-call
free-gate admission race, reads and mutations while busy, acquisition files and cleanup, preflight
failures, time-lapse idle periods, and transport health. Finish with **File > Exit**, then verify the
process, ports, and worker threads closed.

Every ordinary mutation must first return an accepted `processing` operation. The test must poll
that same operation ID to `completed` or `failed` before issuing the next ordinary mutation. Read
command output directly. Emergency commands remain immediate, but still poll the operation they
stopped before continuing.

If TCP polling times out, do not rerun the accepted mutation. Record whether a warning dialog, GUI
update, acquisition preflight, or slow network-storage check is active. A timeout on a read-only
`get_progress` call is safe to retry after Core becomes responsive; the accepted mutation is not.
Any `QTcpSocket has been deleted` traceback is a transport cleanup failure, even if later calls
recover.

The focused preflight test uses reconnect-per-call by default. Set
`MESOSPIM_LIVE_TCP_PERSISTENT_CLIENT=1` to run the same cases through one authenticated connection.
Do not set it for the concurrency group, which intentionally needs independent clients.

Offline and real-PyQt success is required but does not replace this DemoStage check.
