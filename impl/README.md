# Remote Control source and unit tests

`impl/` is the readable source for the five new Remote Control modules. The upstream patch is
generated from these files. A small PyQt replacement lets most behavior run without mesoSPIM,
hardware, or a display.

## Layout

```text
mesoSPIM/src/
  mesoSPIM_RemoteControl_Config.py     constants and defaults
  mesoSPIM_RemoteControl_Dispatcher.py validation, operation state, replies, and strict JSON
  mesoSPIM_RemoteControl_Commands.py   limits and all 53 named commands
  mesoSPIM_RemoteControl_Servers.py    TCP, MCP, request routing, and startup
  mesoSPIM_RemoteControl_GUI.py        the operator-facing controls
  utils/acquisitions.py                test-only replacement for upstream acquisition classes
tests/
  conftest.py                          small PyQt replacement used by unit tests
  fakes.py                             test Core, state, signals, and configuration
  test_remote_control.py
  test_coverage.py
```

## Run the unit tests

```bash
cd impl
python -m pytest tests -q
```

These tests cover all commands, accepted and rejected inputs, movement and hardware-setting limits,
operation polling, simultaneous calls, TCP framing and authentication, MCP HTTP handling, shutdown,
and startup checks.

Stage moves have a specific asynchronous contract:

1. Validate the request and reserve an operation ID.
2. Return `processing` without waiting for the stage.
3. Issue the stage move with `wait_until_done=False`.
4. Check mesoSPIM position readback until the target is reached.
5. Report `completed` or `failed` through `get_progress`.

The unit tests use immediate fake timers. The real timer and transport ordering is tested by
`pull_request/tests/real_pyqt_smoke.py` and `real_pyqt_transport_smoke.py`.

## Upstream integration

Use the generated patch in `pull_request/`. The exact Core and MainWindow additions are documented
in [`INTEGRATION.md`](INTEGRATION.md). The acquisition file under `impl/` is only a test helper;
upstream mesoSPIM provides the real classes.
