"""Shared test infrastructure.

Resolves ``MESOSPIM_RC_SOURCE_ROOT`` once, in the one place every support module can
reach: ``patch_loader`` builds the modules under test from it, ``acquisitions`` imports
production's real acquisition classes from it, and ``test_transport_security`` imports
the packaged servers from it. Resolving it in the package body means the root is on
sys.path before any support module runs, whichever module a test file imports first.

Unset, everything falls back to the patch file, so an unconfigured run still works.
"""

from __future__ import annotations

import os
import sys
from pathlib import Path

_CONFIGURED = os.environ.get("MESOSPIM_RC_SOURCE_ROOT", "").strip()
SOURCE_ROOT = Path(_CONFIGURED) if _CONFIGURED else None

if SOURCE_ROOT is not None:
    if not (SOURCE_ROOT / "mesoSPIM" / "src").is_dir():
        raise RuntimeError(f"MESOSPIM_RC_SOURCE_ROOT={_CONFIGURED!r} has no mesoSPIM/src directory")
    if str(SOURCE_ROOT) not in sys.path:
        sys.path.insert(0, str(SOURCE_ROOT))


def _install_fake_pyqt5():
    """The five-module architecture imports PyQt5 at import time (the Servers module wires Qt
    signals). Install a minimal fake so the Qt-free dispatcher/commands and the importable parts of
    Servers load without a real Qt. singleShot(0, fn) fires fn() immediately, so a WAIT command's
    deferred body runs synchronously in-test. Mirrors impl/tests/conftest.py."""
    if "PyQt5" in sys.modules:
        return
    import types

    qtcore = types.ModuleType("PyQt5.QtCore")

    class QObject:
        def __init__(self, parent=None):
            self._parent = parent

        def thread(self):
            return _MAIN_THREAD

    class _Signal:
        def __init__(self):
            self._slots = []

        def connect(self, slot, *a, **k):
            self._slots.append(slot)

        def disconnect(self, slot=None):
            self._slots = [] if slot is None else [s for s in self._slots if s is not slot]

        def emit(self, *args):
            for slot in list(self._slots):
                slot(*args)

    def pyqtSignal(*_a, **_k):
        class _Descriptor:
            def __set_name__(self, owner, name):
                self._name = "_sig_" + name

            def __get__(self, obj, owner=None):
                if obj is None:
                    return self
                if not hasattr(obj, self._name):
                    setattr(obj, self._name, _Signal())
                return getattr(obj, self._name)

        return _Descriptor()

    def pyqtSlot(*_a, **_k):
        return lambda fn: fn

    class _Thread:
        pass

    _MAIN_THREAD = _Thread()

    class QThread:
        @staticmethod
        def currentThread():
            return _MAIN_THREAD

    class QTimer:
        @staticmethod
        def singleShot(_msec, fn):
            fn()

    class _Qt:
        QueuedConnection = 0
        DirectConnection = 1

    qtcore.QObject = QObject
    qtcore.pyqtSignal = pyqtSignal
    qtcore.pyqtSlot = pyqtSlot
    qtcore.QThread = QThread
    qtcore.QTimer = QTimer
    qtcore.Qt = _Qt

    qtnetwork = types.ModuleType("PyQt5.QtNetwork")
    qtnetwork.QTcpServer = object
    qtnetwork.QHostAddress = object

    pyqt5 = types.ModuleType("PyQt5")
    pyqt5.QtCore = qtcore
    pyqt5.QtNetwork = qtnetwork
    sys.modules["PyQt5"] = pyqt5
    sys.modules["PyQt5.QtCore"] = qtcore
    sys.modules["PyQt5.QtNetwork"] = qtnetwork


_install_fake_pyqt5()
