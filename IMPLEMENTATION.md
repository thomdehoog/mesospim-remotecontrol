# Remote Control cleanup — implementation guide

Companion to `REFACTOR_PLAN_REVIEWED.md` (policy, scope, what is forbidden). **This document is the
executable detail**: the code to write, in order, with the reason for each piece.

Work in `Z:\zmbstaff\10374\Protocols_Notes\thom\notes\repositories\rc-review\` (base `b3c9638`, patch
applied).

```text
git:  C:\ProgramData\MinicondaZMB\Library\bin\git.exe
env:  mesospim-control
```

Both verified on this machine. There is **no `envs\builder` conda environment** — that directory does
not exist, so any path under it fails on step one. `git --version` from the path above returns
`2.53.0.windows.1`. `mesospim-control` is the only env with **both** pytest and PyQt5.

Commit order: Phase 0 → 1 → 2 → 3 → 4 → 5. **Every phase ends green.**

## Code style — binding, see `REFACTOR_PLAN_REVIEWED.md`

The code below is written to these rules and must stay that way. They exist so the result can be
**extended**, not admired for its brevity.

- **Docstrings carry the "why"** — the reason, the contract, the trap. Not a restatement of the code.
- **No `#` blocks stacked above statements.** A one-line note goes at the end of the line it explains;
  a paragraph goes in the enclosing docstring. No ASCII section banners.
- **No filler whitespace.** Blank lines only where they separate genuinely distinct steps; never for
  decoration, never padding a comment.
- **Explicit over clever.** Named helpers. No lambdas, closures or factories hiding behaviour.
- **Adding a command stays a four-step recipe** (handler → `COMMANDS` → `_HINTS` → `VALID_CASES`), and
  that recipe is stated in the command module's docstring. Nothing here may lengthen it.

---

# Phase 0 — make the tests able to fail

Two defects make the current suite incapable of validating this refactor.

1. **It tests the wrong code.** `tests/support/patch_loader.py` builds the modules from the *patch
   file*, not the source tree. `test_validation.py`, `test_adversarial.py` and `test_viability.py`
   import through it with no escape hatch. You could refactor the tree completely and the unit suite
   would stay green, because it is reading the old patch.
2. **Its state fakes are plain dicts.** Production `mesoSPIM_StateSingleton`
   (`mesoSPIM_State.py:103-172`) exposes only `__getitem__` (raising `KeyError`), `__setitem__`,
   `__len__`, `set_parameters`, `get_parameter_dict`, `get_parameter_list`, `block_signals`. There is
   **no `.get()`, no `__contains__`, no `__delitem__`**. A dict fake green-lights code that dies on the
   instrument.

## 0.1 `tests/support/patch_loader.py`

```python
"""Load the Remote Control modules from the source tree, or from the patch."""
from __future__ import annotations

import os
import sys
import tempfile
from pathlib import Path

PULL_REQUEST_ROOT = Path(__file__).resolve().parents[2]
PATCH = next(PULL_REQUEST_ROOT.glob("0001-*.patch"))
_MODULE_DIRECTORY = tempfile.TemporaryDirectory(prefix="rc_under_test_")

MODULES = (
    "mesoSPIM_RemoteControl_ValidateAndRunCommands",
    "mesoSPIM_RemoteControl_Servers",
)

#: Where the modules under test came from. Printed in the pytest header so a refactor
#: can never be validated against stale patch text by accident.
SOURCE = "patch"


def extract(path_suffix: str) -> str:
    """Return one patched file body from its new-file hunk."""
    # ... unchanged from today ...


def _sources() -> dict:
    """Return {module name: source text}, from the tree if MESOSPIM_RC_SOURCE_ROOT is set.

    The patch file is the default so an unconfigured run still works. When a source root is
    given, mesoSPIM/src is put on sys.path here: _make_acquisition_list falls back to
    `from utils.acquisitions import ...` and FakeState needs AcquisitionList, and neither
    should depend on the caller's working directory.
    """
    global SOURCE
    root = os.environ.get("MESOSPIM_RC_SOURCE_ROOT", "").strip()
    if not root:
        SOURCE = "patch"
        return {name: extract(f"mesoSPIM/src/{name}.py") for name in MODULES}
    src = Path(root) / "mesoSPIM" / "src"
    if not src.is_dir():
        raise RuntimeError(f"MESOSPIM_RC_SOURCE_ROOT={root!r} has no mesoSPIM/src directory")
    if str(src) not in sys.path:
        sys.path.insert(0, str(src))
    SOURCE = str(src)
    return {name: (src / f"{name}.py").read_text(encoding="utf-8") for name in MODULES}


def load_modules():
    """Materialize and import the two modules once for the test session.

    They are imported flat, with no package, so the relative import inside the servers module
    is rewritten whichever source it came from. The temp directory is inserted onto sys.path
    last so its copies win over anything under mesoSPIM/src.
    """
    sources = _sources()
    servers = sources["mesoSPIM_RemoteControl_Servers"]
    for old_import in (
        "from .mesoSPIM_RemoteControl_ValidateAndRunCommands import",
        "from mesoSPIM.src.mesoSPIM_RemoteControl_ValidateAndRunCommands import",
    ):
        servers = servers.replace(
            old_import, "from mesoSPIM_RemoteControl_ValidateAndRunCommands import"
        )
    sources["mesoSPIM_RemoteControl_Servers"] = servers
    module_dir = Path(_MODULE_DIRECTORY.name)
    for name, text in sources.items():
        (module_dir / f"{name}.py").write_text(text, encoding="utf-8")
    sys.path.insert(0, str(module_dir))
    import mesoSPIM_RemoteControl_ValidateAndRunCommands as validator_module
    import mesoSPIM_RemoteControl_Servers as servers_module
    return validator_module, servers_module


vrc, srv = load_modules()
```

## 0.2 `tests/conftest.py` — append

```python
def pytest_report_header(config):
    from tests.support import patch_loader
    return f"remote-control modules under test: {patch_loader.SOURCE}"
```

Every test file imports `from tests.support.… import …` (`test_validation.py:24`,
`test_adversarial.py:26`, `test_viability.py:23`), so the conftest must use the same rooted form.

Run the offline suite as:

```powershell
$env:MESOSPIM_RC_SOURCE_ROOT = 'Z:\zmbstaff\10374\Protocols_Notes\thom\notes\repositories\rc-review'
python -m pytest tests -m offline
```

The header must read `modules under test: …\rc-review\mesoSPIM\src`. If it says `patch`, stop.

## 0.3 `tests/support/fake_state.py` — new file

```python
"""A state object faithful to production mesoSPIM_StateSingleton.

Production state (mesoSPIM/src/mesoSPIM_State.py:103-172) is a QObject exposing ONLY
__getitem__ (raising KeyError), __setitem__, __len__, set_parameters, get_parameter_dict,
get_parameter_list and block_signals. There is no .get(), no __contains__, no __delitem__.

A plain-dict fake silently passes code that dies on the instrument, so every state-bearing
fake uses this instead. The mutex is production's concern, not the fake's; the observable
semantics are what matter here.
"""
from __future__ import annotations

from copy import deepcopy

DEFAULTS = {
    "state": "idle",
    "position": {"x_pos": 0.0, "y_pos": 0.0, "z_pos": 0.0, "f_pos": 0.0, "theta_pos": 0.0},
    "selected_row": 0,
    "laser": "488 nm",
    "intensity": 10,
    "filter": "Empty",
    "zoom": "1x",
    "shutterconfig": "Both",
    "shutterstate": False,
    "folder": "",
    "snap_folder": "",
    "ETL_cfg_file": "",
    "etl_l_amplitude": 0.0,
    "etl_l_offset": 0.0,
    "etl_r_amplitude": 0.0,
    "etl_r_offset": 0.0,
}


class FakeState:
    """The production access surface, and nothing more.

    Deep-copies DEFAULTS so each fake owns its nested values and a mutation cannot leak
    between tests. `acq_list` is seeded because it is a real default key in production
    (mesoSPIM_State.py:40) -- the acquire_finish bug hinges on it being present.
    """

    def __init__(self, **overrides):
        from utils.acquisitions import AcquisitionList
        values = deepcopy(DEFAULTS)
        values.update(overrides)
        values.setdefault("acq_list", AcquisitionList([]))
        self._state_dict = values

    def __getitem__(self, key):
        return self._state_dict[key]  # KeyError on miss, like production

    def __setitem__(self, key, value):
        self._state_dict[key] = value

    def __len__(self):
        return len(self._state_dict)

    def set_parameters(self, values):
        self._state_dict.update(values)

    def get_parameter_dict(self, keys):
        """Index every requested key, raising KeyError on a miss, as production does.

        mesoSPIM_State.py:146-152. Do not make this lenient -- a forgiving fake is what let
        the production-only failures through in the first place.
        """
        return {key: self._state_dict[key] for key in keys}

    def get_parameter_list(self, keys):
        """As get_parameter_dict, in list form. mesoSPIM_State.py:163-169."""
        return [self._state_dict[key] for key in keys]

    def block_signals(self, _boolean):
        pass
```

`_state_dict` is public-by-convention on purpose: `_state_snapshot(core, keys=None)` reads it to
enumerate all keys (`ValidateAndRunCommands.py:328-336`) — that is the `get_state_all` no-keys path.

**Consequence to know:** `get_state_all` with an unknown key now raises `KeyError` through the fake,
exactly as it does on the instrument. That is existing behaviour, newly reproducible. Add a test.

## 0.4 `tests/support/fake_core.py`

`get_config` and `acquire_start` are both in `VALID_CASES` (`contracts.py:9,52`) and
`test_valid_transports.py:71` asserts every case succeeds over both transports. The moment
`_camera_pixels` can raise, the whole suite reds for a reason unrelated to the fix — unless the config
fakes have a camera.

```python
"""Small shared Core configurations used by offline tests."""
from tests.support.fake_state import FakeState


class UnitConfig:
    filterdict = {"Empty": 0, "515LP": 1}
    zoomdict = {"1x": 1, "2x": 2}
    laserdict = {"488 nm": 0, "561 nm": 1}
    shutteroptions = ["Left", "Right", "Both"]
    camera_parameters = {"x_pixels": 2048, "y_pixels": 2048}      # NEW
    stage_parameters = {
        "x_min": -25000, "x_max": 25000,
        "y_min": -50000, "y_max": 50000,
        "z_min": -25000, "z_max": 25000,
        "f_min": 0, "f_max": 98000,
        "y_load_position": 1000,
        "y_unload_position": -1000,
        "x_center_position": 0,
        "z_center_position": 0,
    }


class TransportConfig(UnitConfig):
    stage_parameters = {**UnitConfig.stage_parameters, "theta_min": -999, "theta_max": 999}


class NoCameraConfig(UnitConfig):
    """Camera-dimension error tests ONLY."""
    camera_parameters = {}


class BadCameraConfig(UnitConfig):
    """Camera-dimension error tests ONLY."""
    camera_parameters = {"x_pixels": "wide", "y_pixels": 2048}


class UnitCore:
    def __init__(self):
        self.cfg = UnitConfig()
        self.state = FakeState()
        self.calls = []

    def start(self, *args, **kwargs):
        self.calls.append(("start", args, kwargs))
```

`UnitCore.cfg` becomes an instance attribute (it is a bare class attribute today, `fake_core.py:34`);
check the call sites that do `UnitCore()` vs `UnitCore` and fix accordingly.

## 0.5 Migrate every state-bearing fake

| File | Line | Today | Change |
|---|---|---|---|
| `tests/unit/test_validation.py` | 238-247, 269-271 | dict state | `FakeState()` |
| `tests/unit/test_adversarial.py` | 33 | dict state | `FakeState()` |
| `tests/integration/test_transport_security.py` | 56-91 | dict state | `FakeState()` |
| `tests/integration/test_viability.py` | 33-38 | dict state | `FakeState()` |
| `mesoSPIM/src/test_remote_control_validation.py` | 191-197 | dict state | `FakeState`-equivalent, inlined (this file ships in the patch and cannot import from `tests/`) |

`test_transport_security.py:119` calls `self.state.update(args[0])` — a dict-only method production
does not have. Change it to `self.state.set_parameters(args[0])`. **Do not add `.update()` to
`FakeState`**; that would re-open the hole.

Also in that file, the session monkeypatching (`:97, 709, 722, 734, 745, 766, 809, 819, 869`) currently
pokes `core._mesospim_remote_operation`. After Phase 2 those become
`core._remote_session["operation"] = …`. Same for `test_valid_transports.py:53`,
`test_validation.py:243`, and the in-patch `test_remote_control_validation.py:200`.

Treat this as a complete state migration, not a search-and-replace of operation assignments:

- every fake/reset creates a fresh `{"operation": None, "counter": 0, "snapshot": None}` container;
- snapshot fixtures use `session["snapshot"]`, and saved-list fixtures use
  `session["prev_acq_list"]`;
- cleanup resets or replaces the container instead of deleting legacy attributes; and
- after Phase 2, `rg -n '_mesospim_remote_' tests mesoSPIM/src/test_remote_control_validation.py`
  returns no legacy fixture, reset, or cleanup references.

**`SimCore` keeps its plain dict** (`ValidateAndRunCommands.py:1220`). It is a small production
smoke-check double for limit enforcement, not a state-contract fixture. Giving it a second production
state emulation is exactly the bloat we are removing. It only has to keep working.

## 0.6 The regression tests that must go red

```python
# tests/unit/test_acquire_finish.py  (new)
from tests.support.fake_core import UnitCore
from tests.support.patch_loader import vrc


def test_standalone_acquire_finish_leaves_the_list_alone():
    """A client may call acquire_finish without acquire_start. It must not touch the list."""
    core = UnitCore()
    before = core.state["acq_list"]
    vrc.run(core, "acquire_finish", {})
    assert core.state["acq_list"] is before        # FAILS today: becomes None


def test_acquire_start_then_finish_restores_the_exact_object():
    core = UnitCore()
    before = core.state["acq_list"]
    vrc.run(core, "acquire_start", {"acquisition": {"filename": "a.tif", "planes": 1}})
    assert core.state["acq_list"] is not before
    vrc.run(core, "acquire_finish", {})
    assert core.state["acq_list"] is before
```

```python
# tests/unit/test_camera_dimensions.py  (new)
import pytest
from tests.support.fake_core import BadCameraConfig, NoCameraConfig, UnitCore
from tests.support.patch_loader import vrc


def test_get_config_reports_the_configured_sensor():
    core = UnitCore()
    assert vrc.run(core, "get_config", {})["camera"] == {"pixels_x": 2048, "pixels_y": 2048}


@pytest.mark.parametrize("cfg", [NoCameraConfig(), BadCameraConfig()])
@pytest.mark.parametrize("command,args", [
    ("get_config", {}),
    ("acquire_start", {"acquisition": {"filename": "a.tif", "planes": 1}}),
])
def test_bad_camera_config_fails_clearly(cfg, command, args):
    core = UnitCore()
    core.cfg = cfg
    with pytest.raises(ValueError, match="camera_parameters"):
        vrc.run(core, command, args)


def test_a_failed_acquire_start_does_not_touch_state_or_schedule(monkeypatch):
    """The whole point of the reordering: no rollback needed because nothing was done."""
    core = UnitCore()
    core.cfg = NoCameraConfig()
    before = core.state["acq_list"]
    scheduled = []
    monkeypatch.setattr(vrc, "_defer", lambda *a, **k: scheduled.append(a))
    with pytest.raises(ValueError):
        vrc.run(core, "acquire_start", {"acquisition": {"filename": "a.tif", "planes": 1}})
    assert core.state["acq_list"] is before
    assert scheduled == []
```

Plus: read commands work without `.get()` (drive `hello`, `ping`, `get_state`, `get_position`,
`get_info`, `get_progress` against `FakeState`), and `get_progress` returns stable `null`s for the four
unavailable keys through the production `KeyError` shim.

**Run these against the unrefactored tree.** `test_standalone_acquire_finish_leaves_the_list_alone`
must fail with `acq_list is None`. If it passes, your fake is still a dict — fix that before going on.

## 0.7 Apply the two fixes (§4.1–4.3), go green, commit.

---

# Phase 1 — `mesoSPIM_RemoteControl_Tab.py` (new)

The GUI is **moved, not redesigned**: same object names, labels, fonts, margins, spacing, defaults,
port switching, enable rules, status text, tab position.

```python
"""The Remote Control tab: owns its widgets, its settings, and the MCP child process.

Follows the project's convention for optional features (mesoSPIM_Optimizer,
ProcessorChainWindow): take the MainWindow as parent and reach back through it. MainWindow
keeps only a handle and a teardown call.
"""
import secrets

from PyQt5 import QtCore, QtWidgets

TCP_PORT = 42000
MCP_PORT = 42100


class RemoteControlTab(QtWidgets.QWidget):
    """Self-contained Remote Control GUI.

    The two signals are emitted to Core through a queued connection so the server's socket is
    created on the Core's own thread, which is where it must live. MainWindow keeps only a
    handle to this object and calls shutdown() on close.
    """

    sig_start_remote_control = QtCore.pyqtSignal(str, int, str)
    sig_stop_remote_control = QtCore.pyqtSignal()

    def __init__(self, parent):
        super().__init__(parent.TabWidget)
        self.main_window = parent  # modal parent; using self would re-centre the dialogs
        self.core = parent.core
        self.package_directory = parent.package_directory
        self.running = False
        self.mode = "TCP"
        self.host = "127.0.0.1"
        self.port = TCP_PORT
        self.token = "smart_mesospim"
        self._pending_mcp = None
        self._mcp_process = None
        self.setObjectName("RemoteControlTabWidget")
        self._build_ui()
        self.sig_start_remote_control.connect(
            self.core.start_remote_control, type=QtCore.Qt.QueuedConnection)
        self.sig_stop_remote_control.connect(
            self.core.stop_remote_control, type=QtCore.Qt.QueuedConnection)
        self.core.sig_remote_control_started.connect(self.on_started)
        index = parent.TabWidget.indexOf(parent.TimelapseTabWidget)
        if index >= 0:
            parent.TabWidget.insertTab(index + 1, self, "Remote Control")
        else:
            parent.TabWidget.addTab(self, "Remote Control")
        self.update_mode_note()
        self.refresh()

    def _build_ui(self):
        """Build the tab exactly as MainWindow built it: same object names, labels, fonts,
        margins, spacing and defaults. This is a move, not a redesign."""
        layout = QtWidgets.QVBoxLayout(self)
        layout.setContentsMargins(10, 10, 10, 10)
        layout.setSpacing(10)

        group = QtWidgets.QGroupBox("Setup remote control", self)
        group.setObjectName("RemoteControlSetupGroupBox")
        font = group.font()
        font.setPointSize(12)
        group.setFont(font)

        form = QtWidgets.QFormLayout(group)
        form.setContentsMargins(10, 30, 10, 10)
        form.setSpacing(8)

        self.RemoteControlModeComboBox = QtWidgets.QComboBox(group)
        self.RemoteControlModeComboBox.addItems(["TCP", "MCP"])
        self.RemoteControlModeComboBox.setCurrentText(self.mode)
        self.RemoteControlHostLineEdit = QtWidgets.QLineEdit(self.host, group)
        self.RemoteControlPortLineEdit = QtWidgets.QLineEdit(str(self.port), group)
        self.RemoteControlTokenLineEdit = QtWidgets.QLineEdit(self.token, group)
        self.RemoteControlStatusLabel = QtWidgets.QLabel(group)

        for widget in self._inputs():
            widget.setFont(font)
        self.RemoteControlStatusLabel.setFont(font)

        def label(text):
            item = QtWidgets.QLabel(text, group)
            item.setFont(font)
            return item

        form.addRow(label("Protocol"), self.RemoteControlModeComboBox)
        form.addRow(label("Host"), self.RemoteControlHostLineEdit)
        form.addRow(label("Port"), self.RemoteControlPortLineEdit)
        form.addRow(label("Password"), self.RemoteControlTokenLineEdit)
        form.addRow(label("Status"), self.RemoteControlStatusLabel)

        self.RemoteControlStartButton = QtWidgets.QPushButton("Start", group)
        self.RemoteControlStopButton = QtWidgets.QPushButton("Stop", group)
        self.RemoteControlStartButton.setFont(font)
        self.RemoteControlStopButton.setFont(font)
        buttons = QtWidgets.QHBoxLayout()
        buttons.addWidget(self.RemoteControlStartButton)
        buttons.addWidget(self.RemoteControlStopButton)
        form.addRow(buttons)

        layout.addWidget(group)
        layout.addStretch(1)

        self.RemoteControlStartButton.clicked.connect(self.start)
        self.RemoteControlStopButton.clicked.connect(self.stop)
        self.RemoteControlModeComboBox.currentTextChanged.connect(self.on_mode_changed)

    def _inputs(self):
        return (self.RemoteControlModeComboBox, self.RemoteControlHostLineEdit,
                self.RemoteControlPortLineEdit, self.RemoteControlTokenLineEdit)

    def start(self):
        """Start the server the operator asked for.

        In MCP mode the bridge is fronted by an internal TCP server on an ephemeral port
        (hence port 0) with its own generated token; the operator's token guards the MCP
        endpoint itself. The real TCP port only becomes known when Core reports back, so the
        MCP child cannot be launched until on_started() runs.
        """
        try:
            port = int(self.RemoteControlPortLineEdit.text())
        except ValueError:
            return self._warn("Port must be a number.")
        host = self.RemoteControlHostLineEdit.text().strip() or "127.0.0.1"
        token = self.RemoteControlTokenLineEdit.text().strip()
        if not token:
            return self._warn("Password is required.")
        self.host, self.port, self.token = host, port, token
        self.mode = self.RemoteControlModeComboBox.currentText()
        if self.mode == "MCP":
            internal_token = secrets.token_urlsafe(32)
            self._pending_mcp = (host, port, token, internal_token)
            self.sig_start_remote_control.emit("127.0.0.1", 0, internal_token)
        else:
            self._pending_mcp = None
            self.sig_start_remote_control.emit(host, port, token)

    def stop(self):
        self._stop_mcp()
        self.sig_stop_remote_control.emit()
        self.running = False
        self.refresh()

    def shutdown(self):
        """Tear down on application exit, doing all three things close_app() does today.

        Killing the MCP child without also emitting the queued stop to Core would leave the
        TCP server running; dropping either one orphans a process.
        """
        if self.running:
            self._stop_mcp()
            self.sig_stop_remote_control.emit()
            self.running = False

    def on_started(self, ok, message):
        """Handle Core's queued report of a start attempt.

        On failure the server did NOT start, so the tab must not show "running" -- warn with
        the reason instead. In MCP mode the internal TCP port is parsed out of the success
        message and the child is launched; if the child fails, the TCP server that was just
        started is torn down again so nothing is left half-up.
        """
        pending, self._pending_mcp = self._pending_mcp, None
        if ok and pending is not None:
            try:
                tcp_port = int(str(message).rsplit(":", 1)[1])
            except (IndexError, ValueError):
                ok, message = False, f"Could not read internal TCP port from: {message}"
            else:
                ok, message = self._start_mcp(*pending, tcp_port)
            if not ok:
                self.sig_stop_remote_control.emit()
        self.running = ok
        if not ok:
            self._warn(f"Could not start the server: {message}")
        self.refresh()

    def _start_mcp(self, host, port, token, tcp_token, tcp_port):
        """Launch the MCP bridge child. It is parented to this tab, so tab teardown reaps it."""
        from .mesoSPIM_RemoteControl_Servers import start_mcp_server_process
        ok, message, proc = start_mcp_server_process(
            self, self.package_directory, host, port, token, tcp_token, tcp_port)
        if ok:
            self._mcp_process = proc
        return ok, message

    def _stop_mcp(self):
        from .mesoSPIM_RemoteControl_Servers import stop_mcp_server_process
        if self._mcp_process is not None:
            stop_mcp_server_process(self._mcp_process)
            self._mcp_process = None

    def on_mode_changed(self, _mode):
        self.update_mode_note()
        self.refresh()

    def update_mode_note(self):
        """Swap the default port when the operator switches protocol, but never override a
        port they typed themselves."""
        text = self.RemoteControlPortLineEdit.text()
        if self.RemoteControlModeComboBox.currentText() == "MCP":
            if text == str(TCP_PORT):
                self.RemoteControlPortLineEdit.setText(str(MCP_PORT))
        elif text == str(MCP_PORT):
            self.RemoteControlPortLineEdit.setText(str(TCP_PORT))

    def refresh(self):
        if self.running:
            self.RemoteControlStatusLabel.setText(
                f"{self.mode} running on {self.host}:{self.port}")
        else:
            self.RemoteControlStatusLabel.setText("stopped")
        self.RemoteControlStartButton.setEnabled(not self.running)
        self.RemoteControlStopButton.setEnabled(self.running)
        for widget in self._inputs():
            widget.setEnabled(not self.running)

    def _warn(self, message):
        QtWidgets.QMessageBox.warning(self.main_window, "Remote Control", message)
```

Deleted along the way, all of them guarding an ordering that cannot occur once `__init__` builds the
widgets: `_remote_control_refresh` (a bound method stored on `self` to call a method on `self`), the
`if not hasattr(self, 'RemoteControlStatusLabel')` guard, the five
`getattr(self, '_remote_...', default)` reads, and the dead `self._remote_mode = 'Off'`.

`start_mcp_server_process(parent, …)` already parents the `QProcess` to its first argument — pass the
tab.

## MainWindow — everything that remains

```python
# top of file: DELETE `import secrets` (moves to the tab)

# DELETE the two signals (mesoSPIM_MainWindow.py:83-84):
#     sig_start_remote_control = QtCore.pyqtSignal(str, int, str)
#     sig_stop_remote_control  = QtCore.pyqtSignal()
# DELETE their connections (:205-207). Leaving them makes Core receive every start/stop TWICE.

# at module scope with the other local imports:
from .mesoSPIM_RemoteControl_Tab import RemoteControlTab

# in initialize_and_connect_widgets(), where setup_remote_control_tab() is called today (:623):
self.remote_control = RemoteControlTab(self)

# in close_app(), replacing the three-line remote block (:285-288):
self.remote_control.shutdown()
```

> **Do not add `self.remote_control = None` at `:195`.** `initialize_and_connect_widgets()` runs at
> `:168`, *before* `:195` — the `None` would clobber the constructed tab, `close_app()` would skip
> `shutdown()`, and the MCP `QProcess` would be **orphaned on exit**. The
> optimizer/contrast-window convention is *lazy*; this tab is *eager*. The convention does not
> transfer.

Delete exactly these nine remote-only MainWindow methods: `_start_mcp_server`, `_stop_mcp_server`,
`on_remote_control_started`, `setup_remote_control_tab`, `on_remote_control_mode_changed`,
`update_remote_control_mode_note`, `refresh_remote_control_tab`, `start_remote_control`, and
`stop_remote_control`. Delete the eight `_remote_*` / `_mcp_server_process` attributes. **Do not use
the old `:1281-1470` range as a deletion command:** `choose_snap_folder` begins at that boundary and
is unrelated functionality that must remain.

---

# Phase 2 — one declared Core attribute

## `mesoSPIM_Core.__init__`

Two attributes, declared where every other Core attribute is declared. No comment block above them —
the reasoning belongs in the command module's `_session()` docstring, which is where a maintainer will
be standing when it matters.

```python
self._remote_session = {"operation": None, "counter": 0, "snapshot": None}
self._remote_control_server = None
```

**Why these two lines are what they are** (put this in the Core docstring or the `_session` docstring,
not above the assignment):

- **Owned by Core, not by the server.** A server Stop/Start is one GUI click. If the session lived on
  the server, restarting it mid-acquisition would wipe the busy gate, and a client could then drive
  `move_absolute` into hardware that is still running. Core-owned keeps it fail-closed.
- **Initialized eagerly, never `None`.** The camera thread's snapshot callback and the Core thread's
  dispatch both reach for this. Lazy creation lets both threads build one, and the in-flight operation
  is lost.
- It replaces the four undeclared attributes (`_mesospim_remote_operation`,
  `_mesospim_remote_operation_counter`, `_mesospim_remote_snapshot`, `_mesospim_prev_acq_list`) and
  declares `_remote_control_server`, which Core currently conjures at runtime.

## In `mesoSPIM_RemoteControl_ValidateAndRunCommands.py`

```python
_NOTHING_SAVED = object()          # tells "no saved acq_list" apart from "a saved None"
_IDLE = {"status": "idle"}
_PUBLIC = ("id", "command", "status", "stop_requested", "warnings", "error")


def _session(core):
    """The Core-owned session. CREATES ONLY at the dispatch boundary -- never on a read path.

    The real Core builds this in __init__. Direct-dispatch fakes (which never construct a TCP
    server) get one here, on the Core thread, before any handler runs.
    """
    session = getattr(core, "_remote_session", None)
    if session is None:
        session = {"operation": None, "counter": 0, "snapshot": None}
        core._remote_session = session
    return session


def _read_session(core):
    """Read-only view. Returns None when absent. Safe to call from the camera thread."""
    return getattr(core, "_remote_session", None)


def operation_snapshot(core):
    """The public status of the most recent remote operation."""
    session = _read_session(core)
    operation = session["operation"] if session else None
    if not isinstance(operation, dict):
        return dict(_IDLE)
    return {key: operation[key] for key in _PUBLIC if key in operation}


def _active_operation(core):
    session = _read_session(core)
    operation = session["operation"] if session else None
    if isinstance(operation, dict) and operation.get("status") in _ACTIVE_OPERATION_STATES:
        return operation
    return None


def _begin_operation(core, command, completion=None):
    active = _active_operation(core)
    if active is not None:
        raise BusyError(
            f"system busy: currently processing {active['command']} "
            f"(operation {active['id']})")
    session = _session(core)
    session["counter"] += 1
    operation = {
        "id": f"op-{session['counter']:06d}",
        "command": command,
        "status": "processing",
        "_completion": completion,
    }
    session["operation"] = operation
    return operation
```

`_finish_operation`, `complete_operation` and `fail_operation` change only in how they reach the
operation (`_active_operation` / `_read_session`), not in what they do.

The existing completion values remain `finished`, `snap_image`, `time_lapse`,
`preview_returned_idle`, and `None`. Document them beside `_completion_kind`; do not introduce an
unused runtime constant or new validation that would change accepted behaviour.

`capture_snap_image` **runs on the camera thread** (the server is a plain object, so
`sig_camera_frame` is a direct connection). It must therefore write into a session that already
exists and never create one:

```python
def capture_snap_image(core):
    """Store a completed remote snap as bounded, chunk-readable raw pixels.

    Called from the camera-frame signal ON THE CAMERA THREAD while a remote `snap` waits for
    its image. Live and acquisition frames never enter this store. Never creates the session.
    """
    operation = _active_operation(core)
    if operation is None or operation.get("_completion") != "snap_image":
        return False
    session = _read_session(core)
    if session is None:
        return False
    try:
        ...                                     # unchanged body
        session["snapshot"] = {...}
    except Exception as exc:
        return fail_operation(core, "snap_image", exc)
    return complete_operation(core, "snap_image")
```

`_snap` clears it with `_session(core)["snapshot"] = None`; `_get_snap_image` reads
`_read_session(core)` and raises the existing "no remote snapshot is available" error when absent.

**No class. No operation-ID matching.** Handlers stay `handler(core, args)` and
`handle_tcp_message(core, payload)` is untouched, so the offline harness — which never constructs a
TCP server — keeps working unchanged.

---

# Phase 3 — Core stays put, just tidier

Move `sig_remote_control_started` from mid-class (`:1183`) into the signal declaration block (~`:85`)
where every other Core signal lives. **Keep both slot bodies where they are** — moving them into module
helpers buys an indirection hop and a reverse dependency for zero net lines.

Preserve the whole contract: stop-before-restart; construct on Core's thread; retain on
`self._remote_control_server`; run the fail-closed smoke check before binding; catch and log failures;
`sig_remote_control_started(False, message)` on failure; report the **actual** allocated port;
`sig_remote_control_started(True, "host:port")` on success; on stop close clients, disconnect signals,
close the listener, clear the reference.

---

# Phase 4 — the command module

## 4.0 The module docstring — make the extension point explicit

This module is the one a future maintainer will come to when they want to *add* something. Say how,
at the top, so they do not have to reverse-engineer it:

```python
"""Remote-control command vocabulary and execution.

Transports hand this module a decoded command name plus arguments. The name is checked against
a fixed allowlist, the arguments are validated against the operator's loaded config, and the
matching mesoSPIM Core action runs behind a one-operation busy gate.

To add a command:
    1. write `_your_command(core, args)` returning a JSON-serializable dict;
    2. add one entry to COMMANDS;
    3. add one entry to _HINTS -- this IS the machine-facing contract, because inputSchema is
       only {"type": "object"}; an LLM has nothing else to go on;
    4. add one entry to VALID_CASES in tests/support/contracts.py.
Add an arm to _validate only if the command takes arguments that could reach the hardware.

Two rules that are not negotiable:
    - Never return an error after state has changed or hardware work has been queued. Compute
      everything fallible first (see _acquire_start).
    - Never call .get() on core.state. Production state is a QObject with __getitem__ only;
      use _state_get, which is the Mapping shim, not test scaffolding.
"""
```

## 4.1 `_acquire_start` — validate before you mutate ← **the hazard**

Today: save prev list → overwrite `acq_list` → `_defer(core.start, row=0)` → build the response, whose
**last** expression is `_camera_pixels(...)`. Safe only because that function cannot currently raise.

Once it can (fix #2), a config with missing dimensions means the acquisition list is already destroyed,
`core.start` is already queued on the event loop, and the client gets `error: …`. Worse: `run()` marks
the operation `failed`, which is **not** an active status (`_ACTIVE_OPERATION_STATES`, `:76`), so the
busy gate is **open** and a second `acquire_start` is accepted **while the first is imaging**.

```python
def _acquire_start(core, a):
    """Run one ad-hoc acquisition, stashing the operator's list so acquire_finish can restore it.

    Everything that can fail is computed BEFORE the operator's state is touched or hardware is
    scheduled. This ordering is the whole point: _camera_pixels can now raise, and in the old
    order it raised *after* acq_list had been replaced and core.start queued -- so the client
    got an error while the instrument imaged, and run() marked the operation "failed", which is
    not an active status, leaving the busy gate open for a second acquisition on top of the
    first. If the deferred start cannot even be queued, the list is put back: an error must
    never be returned once state has changed.
    """
    acq = dict(a["acquisition"])
    pixels = list(_camera_pixels(getattr(core, "cfg", None)))
    acq_list = _make_acquisition_list([acq])
    filename = acq.get("filename") or ""
    response = {
        "started": True,
        "scheduled": True,
        "files": [os.path.join(acq.get("folder") or "", filename)] if filename else [],
        "planes": int(acq.get("planes", 1) or 1),
        "pixels": pixels,
    }
    session = _session(core)
    previous = core.state["acq_list"]
    session["prev_acq_list"] = previous
    core.state["acq_list"] = acq_list
    try:
        _defer(core.start, row=0)
    except Exception:
        core.state["acq_list"] = previous
        session.pop("prev_acq_list", None)
        raise
    return response
```

**Module-wide rule: never return an error after state has changed or hardware work has been queued.**
Add a regression test that monkeypatches `_defer` to raise, then asserts that the original
`acq_list` is restored, `prev_acq_list` is absent, and `core.start` was not called. In normal success
tests, monkeypatch `_defer` to a deterministic no-op or immediate call; do not depend on a Qt event
loop in this unit-level phase.

## 4.2 `_acquire_finish` — no-op when nothing was saved ← **approved fix #3**

```python
def _acquire_finish(core, a):
    """Restore the operator's acquisition list, if acquire_start saved one.

    A client may call this standalone. It must then change nothing -- the old code tried
    `del state["acq_list"]`, which production cannot do (no __delitem__), and the broad
    `except` turned that into `state["acq_list"] = None`.
    """
    prev = _session(core).pop("prev_acq_list", _NOTHING_SAVED)
    if prev is not _NOTHING_SAVED:
        core.state["acq_list"] = prev
    return {"state": _state_get(core, "state")}
```

Gone: the `del`, the broad `except`, the `had` flag.

## 4.3 `_camera_pixels` — no fabrication ← **approved fix #2**

```python
def _camera_pixels(cfg):
    """The configured sensor size. No guessing: a config without one is a broken config."""
    params = _cfg_dict(cfg, "camera_parameters")
    try:
        return int(params["x_pixels"]), int(params["y_pixels"])
    except (KeyError, TypeError, ValueError) as exc:
        raise ValueError(f"camera_parameters is missing or invalid: {exc}") from exc
```

The old code fell back to `cfg.camera_x_pixels`, an attribute in **no config and no source file**,
then to a hardcoded **2048** when configuration data was missing or malformed. A valid configured
5056 x 2960 camera was already reported correctly; do not describe that normal path as broken. Test
missing `x_pixels`, missing `y_pixels`, invalid `x_pixels`, and invalid `y_pixels` through both callers
(`_get_config` and `_acquire_start`).

*(Known, pre-existing, out of scope: `pixels_x` is the unbinned sensor size while `get_snap_image`'s
`shape` is binned. Do not drift into fixing that here.)*

## 4.4 Collapse the four identical setters

`_set_camera` / `_set_etl` / `_set_galvo` / `_set_laser_timing` (`:425-455`) differ only by a key tuple,
and `_validate` already treats all four identically (`:1074-1076`).

```python
_SETTING_GROUPS = {
    "set_camera": ("camera_exposure_time", "camera_line_interval", "camera_delay_%",
                   "camera_pulse_%", "camera_display_live_subsampling",
                   "camera_display_acquisition_subsampling", "camera_sensor_mode",
                   "camera_binning"),
    "set_etl": ("etl_l_delay_%", "etl_l_ramp_rising_%", "etl_l_ramp_falling_%",
                "etl_l_amplitude", "etl_l_offset", "etl_r_delay_%", "etl_r_ramp_rising_%",
                "etl_r_ramp_falling_%", "etl_r_amplitude", "etl_r_offset"),
    "set_galvo": ("galvo_l_frequency", "galvo_l_amplitude", "galvo_l_offset",
                  "galvo_l_duty_cycle", "galvo_l_phase", "galvo_r_frequency",
                  "galvo_r_offset", "galvo_r_duty_cycle", "galvo_r_phase",
                  "galvo_amp_scale_w_zoom"),
    "set_laser_timing": ("laser_l_delay_%", "laser_l_pulse_%",
                         "laser_r_delay_%", "laser_r_pulse_%"),
}


def _set_group(core, a, keys):
    core.state_request_handler(_settings_from_args(a, keys))
    return {}


def _set_camera(core, a):
    return _set_group(core, a, _SETTING_GROUPS["set_camera"])


def _set_etl(core, a):
    return _set_group(core, a, _SETTING_GROUPS["set_etl"])


def _set_galvo(core, a):
    return _set_group(core, a, _SETTING_GROUPS["set_galvo"])


def _set_laser_timing(core, a):
    return _set_group(core, a, _SETTING_GROUPS["set_laser_timing"])
```

Keep named plain callables in `COMMANDS`, which makes tracebacks and command-to-handler inspection
obvious. Do not use `functools.partial` or a closure factory. MCP descriptions come from `_HINTS`, not
from handler docstrings; docstring poisoning is therefore not the reason for this choice.

## 4.5 Everything else in this module

- **One** operation-snapshot builder (the same six-key comprehension is written at `:90-96` **and**
  `:214-218`) — done by `operation_snapshot` in §2.
- **One** acq-list resolver, shared by `_get_disk_space` (`:707`) and `_check_motion_limits` (`:713`).
- **Inline** `_snapshot_metadata` (`:535-539`) — one caller, redoing a guard `_get_snap_image` already did.
- **Keep** the explicit dtype/shape/`tobytes` guard in `capture_snap_image` (`:164-173`). Direct
  attribute access swaps a named `TypeError("snapshot image must expose dtype, shape, and tobytes()")`
  for a bare `AttributeError` in the client's `operation["error"]`, which contradicts the stable-error
  requirement. Four lines is the right price. **Keep** the `hasobject` guard too — object arrays'
  `tobytes()` returns pointer garbage rather than raising.
- **Delete** `_procedure` (`:734-735`), `COMMANDS["procedure"]` (`:882`), `_HINTS["procedure"]` (`:909`),
  and the `_validate` comment mentioning it (`:1128`). Then update `contracts.py:70`,
  `test_valid_transports.py:42,65-69`, `test_all_commands.py:7,343,352,365`, `README.md:149`,
  `REMOTE_CONTROL_REFERENCE.md:148`, and change all three semantic command-count assertions from 56
  to 55: `contracts.py:138`, `test_valid_transports.py:38`, and `test_all_commands.py:39`.
  Replace the procedure-specific tests with assertions that it is **absent** from `COMMANDS`,
  capabilities, contracts and MCP `tools/list`. Delete no test file.
  Finish with repository-wide searches for `procedure`, `56`, and `all commands`; inspect each hit
  rather than blindly replacing unrelated corpus sizes or adversarial-test counts.
- **Keep `_HINTS`.** Add `assert set(_HINTS) <= set(COMMANDS)` and fix the `get_info` hint, which
  promises snapshot metadata `_get_info` does not return. `_HINTS` is a deliberate **subset** — 21
  entries, 20 after `procedure`. **Never assert there are 55 hints.**
- **Keep** `_item_get` / `_state_get` (may become one short helper, honestly commented as the Mapping
  shim). **Never** call `.get()` on `core.state`.
- **Keep `_jsonable`.** It already stringifies unknown values *and* coerces non-string dict keys, which
  `json.dumps(default=str)` cannot do — `default=` is never applied to keys.
- **Keep** `_LIMITS` at import time; `SimCore` + `self_test` (change "prove" → "smoke-check" only);
  strict JSON; snapshot chunking; emergency commands; `_completion_kind`'s `hasattr` guards;
  `_stop_activity`'s idle check; `_move_relative`'s serial-worker path.
- **Keep** the three mode wrappers (`:784-793` — nine obvious lines) and the three stage presets
  (`:796-827` — load/unload require their key, center accepts either). Collapsing either is churn.
- **`get_progress`** keeps its shape. The four fields stay `null`; document them as currently
  unavailable and assert the stable null shape **against a `FakeState`**, not a dict — with a dict the
  `.get()` branch is taken and the production `KeyError` path is never exercised.

---

# Phase 5 — the server module

```python
# __init__ -- replaces the hand-maintained connect list (:155-163)
self._connections = [
    (getattr(core, "sig_finished", None), self._on_core_finished),
    (getattr(core, "sig_time_lapse_finished", None), self._on_time_lapse_finished),
    (getattr(core, "sig_time_lapse_cancelled", None), self._on_time_lapse_finished),
    (getattr(getattr(core, "camera_worker", None), "sig_camera_frame", None),
     self._on_camera_frame),
]
for signal, slot in self._connections:
    if signal is not None:
        signal.connect(slot)

# stop() -- the mirror (:265-276). The two lists can no longer drift apart.
for signal, slot in self._connections:
    if signal is not None:
        try:
            signal.disconnect(slot)
        except (TypeError, RuntimeError):
            pass
```

Also: delete `_close` (`:227-229`) and call `_drop_client` directly; extract **one** shared frame-header
validator (canonical numeric header + size limit) used by both `read_frame` (`:54-75`) and
`FrameDecoder.frames` (`:95-110`).

**Keep** `read_frame` and `FrameDecoder` as separate implementations — blocking socket vs incremental Qt
buffer, different contracts. **Keep** `AuthGate` (the offline harness uses it directly). **Keep** the
path-based MCP launch, the `sys.path` block and both import modes: switching to `-m` would make the
child depend on an inherited working directory, an untested assumption whose failure mode is "the MCP
lane silently does not start". **Keep** the tab out of this module's import graph, or the headless MCP
child starts needing QtWidgets.

## Two limitations to document, not fix

- **Cross-thread session access.** The plain-object server means `capture_snap_image` writes the
  snapshot from the **camera thread** while dispatch mutates session state from the **Core thread**.
  That is today's live-validated behaviour. Preserve it.
- **A snap with no camera frame wedges the gate.** If the frame never arrives, the operation stays
  active with milestone `snap_image`; stop only moves it to `stopping` (still active), and `sig_finished`
  cannot close a `snap_image` milestone. Every mutating command then raises `BusyError` until mesoSPIM
  restarts. Server Stop/Start deliberately preserves the Core-owned session and **is not a recovery
  path**. Operation-ID matching would **not** fix this — it prevents a stale callback completing a
  *newer* operation and does nothing about a completion signal that never arrives. Real recovery needs
  a timeout, a cancel-on-stop transition, or a recovery command. All out of scope.

---

# Verification

```powershell
$env:MESOSPIM_RC_SOURCE_ROOT = 'Z:\zmbstaff\10374\Protocols_Notes\thom\notes\repositories\rc-review'
python -m pytest tests -m offline        # header MUST show the rc-review path, not "patch"
```

Then: the in-tree validation test; demo mode (TCP Start/Stop, MCP Start/Stop, failed MCP start, failed
TCP bind → warns and does **not** show "running"); `self_test` over both transports; the live valid and
adversarial suites; the tab compared against the original for placement, widgets, text, fonts, defaults,
port switching, status text and enable behaviour; exit with MCP running → **no orphan child**; the
instrument pass (snapshot, movement, acquisition, stop, time-lapse). Finally regenerate the patch and
confirm exactly **seven** files:

`mesoSPIM_Core.py`, `mesoSPIM_MainWindow.py`, `mesoSPIM_RemoteControl_Tab.py`,
`mesoSPIM_RemoteControl_ValidateAndRunCommands.py`, `mesoSPIM_RemoteControl_Servers.py`,
`test_remote_control_validation.py`, `pyproject.toml`.

# Size

~1,960 production lines → **~1,680-1,750**. Report production and shipped-test lines separately, and do
not chase the number: readable validation, the hardware-lesson comments, explicit ownership and
fail-closed startup are worth more than a smaller diff.
