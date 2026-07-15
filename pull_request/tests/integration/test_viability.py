"""No-motion viability test: can a limit be violated over either transport?

Stands up the REAL server on both lanes -- framed TCP plus the actual MCP-over-HTTP handler
forwarding to it -- against a recording fake Core, then attacks the smallest value past the
travel envelope. Both lanes must refuse it, and the Core must record **zero moves**: proving
"a limit cannot be violated" must never itself move the stage. Real localhost sockets, no Qt::

    python tests/run.py offline valid
"""
from __future__ import annotations

import socket
import threading
from http.server import ThreadingHTTPServer
from types import SimpleNamespace

import pytest

from tests.support.fake_core import UnitConfig as _Cfg
from tests.support.fake_state import FakeState
from tests.support.patch_loader import srv

_TOKEN = "sekret"


class _RecordingCore:
    def __init__(self):
        self.cfg = _Cfg()
        self.moved = []
        self.state = FakeState()

    def move_absolute(self, sdict, wait_until_done=False):
        self.moved.append(sdict)
        for k, v in sdict.items():
            self.state["position"][k.replace("_abs", "") + "_pos"] = v


def _serve_tcp(core, token):
    """A minimal framed-TCP server using the real srv helpers (like the in-Core one)."""
    listen = socket.socket()
    listen.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listen.bind(("127.0.0.1", 0))
    listen.listen(5)
    listen.settimeout(10)  # bound every server-side wait so a stuck test fails fast, never hangs
    port = listen.getsockname()[1]

    def handle(conn):
        conn.settimeout(10)
        dec, gate = srv.FrameDecoder(), srv.AuthGate(token)
        with conn:
            while True:
                try:
                    data = conn.recv(4096)
                except OSError:
                    return
                if not data:
                    return
                dec.feed(data)
                for fr in dec.frames():
                    text = fr.decode("utf-8")
                    if not gate.passed:
                        conn.sendall(srv.frame("OK" if gate.check(text) else "AUTH-FAILED"))
                    else:
                        conn.sendall(srv.frame(srv.handle_tcp_message(core, text)))

    def serve():
        while True:
            try:
                conn, _ = listen.accept()
            except OSError:
                return
            threading.Thread(target=handle, args=(conn,), daemon=True).start()

    threading.Thread(target=serve, daemon=True).start()
    return listen, port


def test_both_lanes_refuse_an_out_of_limit_move_and_the_stage_never_moves():
    """The probe is ``max + 1`` -- the smallest value past the envelope.

    A REFUSAL is the pass. Validation rejects it before the Core is touched, so proving that
    a limit cannot be violated must never itself move the stage: hence ``core.moved == []``.
    """
    core = _RecordingCore()
    listen, tcp_port = _serve_tcp(core, _TOKEN)
    cfg = SimpleNamespace(token=_TOKEN, quiet=True, timeout=5.0,
                          mesospim_host="127.0.0.1", mesospim_port=tcp_port, mesospim_token=_TOKEN)
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), srv.make_mcp_handler(cfg))
    mcp_port = httpd.server_address[1]
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    try:
        with srv.RemoteControl("127.0.0.1", tcp_port, _TOKEN, timeout=5.0) as scope:
            assert scope.call("hello")["state"] == "idle"
            assert scope.call("self_test")["ok"] is True
            axis, low_high = next(
                (a, r) for a, r in scope.call("get_limits")["enforced"]["axes"].items() if r)
            beyond = low_high[1] + 1
            with pytest.raises(RuntimeError):
                scope.call("move_absolute", targets={axis: beyond})

        over_mcp = srv.mcp_call("127.0.0.1", mcp_port, _TOKEN, "tools/call",
                                "move_absolute", {"targets": {axis: beyond}})
        assert over_mcp["result"]["isError"] is True
    finally:
        httpd.shutdown()
        listen.close()

    assert core.moved == []
