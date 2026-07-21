"""The 'AI Assistant' tab: an output transcript and an input line. Enter submits; the input
disables while a turn runs (single-flight). Interrupt halts a runaway agent. The Acceptor is
acquired lazily on first use — until then the Remote Control transports stay usable, and the
two are mutually exclusive (one controller drives the instrument at a time).

Maintainer (2026):
    Thom de Hoog
    Center for Microscopy and Image Analysis
    thom.dehoog@zmb.uzh.ch
    thomdehoog@gmail.com
"""

from PyQt5 import QtCore, QtWidgets

from .mesoSPIM_AiAssistent import AssistantWorker


class AiAssistentGUI(QtWidgets.QWidget):
    sig_run_turn = QtCore.pyqtSignal(str)

    def __init__(self, parent):
        super().__init__(parent.TabWidget)
        self.main_window = parent
        self.core = parent.core
        self.setObjectName("AiAssistentTabWidget")
        self._worker = None
        self._build_ui()
        index = parent.TabWidget.indexOf(parent.remote_control)   # RemoteControlGUI instance
        if index >= 0:
            parent.TabWidget.insertTab(index + 1, self, "AI Assistant")
        else:
            parent.TabWidget.addTab(self, "AI Assistant")

    def _call_on_core(self, method):
        """Invoke a Core slot on the Core thread (affinity matters — the Acceptor must be
        built there). Blocks until it returns."""
        try:
            same = self.core.thread() is self.thread()
        except AttributeError:                                     # Qt-free test doubles
            same = True
        conn = QtCore.Qt.DirectConnection if same else QtCore.Qt.BlockingQueuedConnection
        QtCore.QMetaObject.invokeMethod(self.core, method, conn)

    def _ensure_worker(self):
        """Acquire the Acceptor (built by Core, on the Core thread) and start the worker, on
        first use. Returns False if a TCP/MCP transport is active (mutually exclusive)."""
        if self._worker is not None:
            return True
        self._call_on_core("start_ai_assistant")
        acceptor = getattr(self.core, "_assistant_acceptor", None)
        if acceptor is None:
            return False
        self._thread = QtCore.QThread(self)
        self._worker = AssistantWorker(acceptor)
        self._worker.moveToThread(self._thread)
        self.sig_run_turn.connect(self._worker.run_turn, QtCore.Qt.QueuedConnection)
        self._worker.sig_reply.connect(self._append_ai)
        self._worker.sig_tool.connect(self._append_tool)
        self._worker.sig_error.connect(self._on_error)
        self._worker.sig_done.connect(self._on_done)
        self._thread.start()
        return True

    def _build_ui(self):
        layout = QtWidgets.QVBoxLayout(self)
        layout.setContentsMargins(10, 10, 10, 10)
        layout.setSpacing(8)

        self.output = QtWidgets.QPlainTextEdit(self)
        self.output.setReadOnly(True)
        self.output.setObjectName("AiAssistentOutput")
        layout.addWidget(self.output, 1)

        row = QtWidgets.QHBoxLayout()
        self.input = QtWidgets.QLineEdit(self)
        self.input.setPlaceholderText("Ask the microscope…")
        self.input.setObjectName("AiAssistentInput")
        self.input.returnPressed.connect(self.on_submit)
        self.interrupt = QtWidgets.QPushButton("Interrupt", self)
        self.interrupt.setEnabled(False)
        self.interrupt.clicked.connect(self.on_interrupt)
        row.addWidget(self.input, 1)
        row.addWidget(self.interrupt)
        layout.addLayout(row)

    def on_submit(self):
        text = self.input.text().strip()
        if not text or not self.input.isEnabled():
            return
        if not self._ensure_worker():
            self._append("—", "Stop the Remote Control transport to use the AI Assistant.")
            return
        self.input.clear()
        self._append("You", text)
        self._set_running(True)
        self.sig_run_turn.emit(text)

    def on_interrupt(self):
        if self._worker is not None:
            self._worker.interrupt()
        self._append("—", "[interrupted]")

    def _set_running(self, running):
        self.input.setEnabled(not running)
        self.interrupt.setEnabled(running)
        if not running:
            self.input.setFocus()

    def _append(self, who, text):
        self.output.appendPlainText(f"{who}:  {text}")

    def _append_ai(self, text):
        self._append("AI", text)

    def _append_tool(self, name, args):
        self._append("·", f"{name}({args})")

    def _on_error(self, message):
        self._append("error", message)

    def _on_done(self):
        self._set_running(False)

    def shutdown(self):
        """Called by MainWindow on app exit: stop the agent, join with a bound so the GUI
        never hangs on an in-flight model call, and release the Core-owned Acceptor."""
        if self._worker is not None:
            self._worker.interrupt()
            self._thread.quit()
            self._thread.wait(3000)
            self._call_on_core("stop_ai_assistant")
