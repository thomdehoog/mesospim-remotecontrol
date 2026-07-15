# Integrating Remote Control into mesoSPIM-control

Almost all code lives in the five new `mesoSPIM_RemoteControl_*` modules. Only two existing
mesoSPIM files change:

- `mesoSPIM_Core.py`: one signal, two attributes, and two short methods.
- `mesoSPIM_MainWindow.py`: import the tab, create it, and close it during shutdown.

No command, validation, network, or polling logic is added to Core or MainWindow.

## 1. `mesoSPIM_Core.py`

Add this signal beside the other Core signals:

```python
sig_remote_control_started = QtCore.pyqtSignal(bool, str)
```

Create the session and transport handle in `__init__`:

```python
self._remote_session = {"operation": None, "counter": 0}
self._remote_control = None
```

Add two Qt methods:

```python
@QtCore.pyqtSlot(str, str, int, str)
def start_remote_control(self, mode, host, port, token):
    from .mesoSPIM_RemoteControl_Servers import start_for_core
    start_for_core(self, mode, host, port, token)

@QtCore.pyqtSlot()
def stop_remote_control(self):
    from .mesoSPIM_RemoteControl_Servers import stop_for_core
    stop_for_core(self)
```

These methods only hand work to the Remote Control server module. They run on the Core thread,
which is the thread allowed to call mesoSPIM Core methods. The session belongs to Core so stopping
and restarting a transport does not erase a running operation.

## 2. `mesoSPIM_MainWindow.py`

Import the tab with the other tab imports:

```python
from .mesoSPIM_RemoteControl_GUI import RemoteControlGUI
```

Create it after the other tabs are available:

```python
self.remote_control = RemoteControlGUI(self)
```

Close it near the start of `close_app`:

```python
self.remote_control.shutdown()
```

`shutdown()` waits until Core has closed the active network listener. It also owns the small bridge
that installs a remote acquisition list in both Core state and the visible acquisition table. This
keeps acquisition-specific code out of MainWindow.

## 3. Behavior that stays inside the new modules

- The operator must start and stop Remote Control by hand.
- One session runs TCP or MCP, never both.
- Every command name and argument is validated before it reaches hardware.
- One remote mutation may run at a time; reads and emergency commands remain available.
- Stage moves return an operation ID promptly and use position readback to determine completion.
- MCP runs inside mesoSPIM; it does not start a child process.
- Startup first verifies the configured movement limits against a simulated Core.

## 4. Verification

The offline suites test all commands, both transports, race handling, input rejection, and operation
state. The real-PyQt transport smoke test verifies that TCP and MCP return a stage operation before
movement completes and remain responsive to polling. The operator-gated Windows DemoStage suite is
the final check for real Core, stage, acquisition, time-lapse, and shutdown behavior.
