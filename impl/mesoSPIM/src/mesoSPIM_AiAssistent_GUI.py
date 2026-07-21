"""The 'AI Assistant' tab: a chat transcript and an input line, styled like a coding-agent chat.

The transcript renders Markdown (the model's bold/lists come through), streams each tool call as it
fires, and shows a 'working' status while a turn runs. Enter submits; the input disables during a
turn (single-flight); Interrupt halts a runaway agent. The Acceptor is acquired lazily on first use
— until then the Remote Control transports stay usable, and the two are mutually exclusive.

Maintainer (2026):
    Thom de Hoog
    Center for Microscopy and Image Analysis
    thom.dehoog@zmb.uzh.ch
    thomdehoog@gmail.com
"""

from PyQt5 import QtCore, QtWidgets

from .mesoSPIM_AiAssistent import AssistantWorker


def _md_escape(text):
    """Escape Markdown-active characters so literal text (a user line, an error) is shown verbatim
    instead of being reinterpreted as formatting."""
    for ch in "\\`*_{}[]()#+-.!":
        text = text.replace(ch, "\\" + ch)
    return text


class AiAssistentGUI(QtWidgets.QWidget):
    sig_run_turn = QtCore.pyqtSignal(str)

    def __init__(self, parent):
        super().__init__(parent.TabWidget)
        self.main_window = parent
        self.core = parent.core
        self.setObjectName("AiAssistentTabWidget")
        self._worker = None
        self._log = []                                            # markdown blocks, oldest first
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
        self._worker.sig_reply.connect(self._on_reply)
        self._worker.sig_tool.connect(self._on_tool)
        self._worker.sig_error.connect(self._on_error)
        self._worker.sig_done.connect(self._on_done)
        self._thread.start()
        return True

    def _build_ui(self):
        layout = QtWidgets.QVBoxLayout(self)
        layout.setContentsMargins(10, 10, 10, 10)
        layout.setSpacing(8)

        font = self.font()
        font.setPointSize(12)                                     # match Remote Control

        self.output = QtWidgets.QTextEdit(self)
        self.output.setReadOnly(True)
        self.output.setObjectName("AiAssistentOutput")
        self.output.setFont(font)
        self.output.setLineWrapMode(QtWidgets.QTextEdit.WidgetWidth)   # wrap; no horizontal scrollbar
        layout.addWidget(self.output, 1)

        self.status = QtWidgets.QLabel("", self)
        self.status.setObjectName("AiAssistentStatus")
        self.status.setFont(font)
        layout.addWidget(self.status)

        row = QtWidgets.QHBoxLayout()
        self.input = QtWidgets.QLineEdit(self)
        self.input.setPlaceholderText("Ask the microscope…")
        self.input.setObjectName("AiAssistentInput")
        self.input.setFont(font)
        self.input.returnPressed.connect(self.on_submit)
        self.interrupt = QtWidgets.QPushButton("Interrupt", self)
        self.interrupt.setFont(font)
        self.interrupt.setEnabled(False)
        self.interrupt.clicked.connect(self.on_interrupt)
        row.addWidget(self.input, 1)
        row.addWidget(self.interrupt)
        layout.addLayout(row)

    # --- transcript rendering (Markdown) ---
    def _render(self):
        # A non-breaking-space paragraph between entries renders as a blank line, so messages get
        # clear breathing room (plain blank lines collapse in Markdown).
        self.output.setMarkdown("\n\n\u00A0\n\n".join(self._log))
        bar = self.output.verticalScrollBar()
        bar.setValue(bar.maximum())                              # keep the newest line in view

    def _append_user(self, text):
        self._log.append(f"**You**\n\n{_md_escape(text)}")
        self._render()

    def _append_assistant(self, text):
        self._log.append(f"**mesoSPIM**\n\n{text}")             # the model's own Markdown renders
        self._render()

    def _append_tool(self, name, args):
        self._log.append(f"`› {name}({args.replace('`', chr(39))})`")
        self._render()

    def _append_error(self, message):
        self._log.append(f"**⚠ error** — {_md_escape(' '.join(message.split()))[:600]}")
        self._render()

    def _append_note(self, message):
        self._log.append(f"*{_md_escape(message)}*")
        self._render()

    # --- input / turn lifecycle ---
    def on_submit(self):
        text = self.input.text().strip()
        if not text or not self.input.isEnabled():
            return
        if not self._ensure_worker():
            self._append_note("Stop the Remote Control transport to use the AI Assistant.")
            return
        self.input.clear()
        self._append_user(text)
        self._set_running(True)
        self.sig_run_turn.emit(text)

    def on_interrupt(self):
        if self._worker is not None:
            self._worker.interrupt()
        self._append_note("[interrupted]")

    def _set_running(self, running):
        self.input.setEnabled(not running)
        self.interrupt.setEnabled(running)
        self.status.setText("mesoSPIM is working…" if running else "")
        if not running:
            self.input.setFocus()

    def _on_reply(self, text):
        self._append_assistant(text)

    def _on_tool(self, name, args):
        self._append_tool(name, args)

    def _on_error(self, message):
        self._append_error(message)

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
