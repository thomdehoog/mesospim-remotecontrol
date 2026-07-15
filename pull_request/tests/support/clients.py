"""The TCP and MCP wire clients the live tests drive the microscope through.

In the five-module architecture the server no longer ships a wire client (Servers.RemoteControl is
the server-side session handle, not a caller). So these clients are re-homed here, built on the
framing primitives that DO survive in Servers (``frame``/``FrameReader``) plus the wire constants
that moved to Config (``OK_MARKER``/``ENCODING``). The wire format is thus still implemented once,
against the very modules under test.
"""
import json
import socket
import urllib.request

from tests.support.patch_loader import srv, config

frame = srv.frame
FrameReader = srv.FrameReader
OK_MARKER = config.OK_MARKER
ENCODING = config.ENCODING


class RemoteControl:
    """The TCP client: connect once, authenticate once, then make named calls.

    The error contract is the protocol's, not Python's: a reply that does not carry the OK marker
    IS the error text, so it is raised verbatim rather than reshaped.
    """

    def __init__(self, host="127.0.0.1", port=42000, token=None, timeout=10.0):
        self._sock = socket.create_connection((host, port), timeout=timeout)
        self._sock.settimeout(timeout)
        self._reader = FrameReader(self._sock)
        if token:
            self._sock.sendall(frame(token))
            reply = self._reader.read().strip()
            if reply != "OK":
                self.close()
                raise RuntimeError(f"TCP authentication failed: {reply}")

    def call(self, name, **arguments):
        """Send ``{name: arguments}`` and return the decoded result."""
        self._sock.sendall(frame(json.dumps({name: arguments})))
        reply = self._reader.read()
        if not reply.startswith(OK_MARKER):
            raise RuntimeError(reply.strip())
        return json.loads(reply[len(OK_MARKER):])

    def close(self):
        self._sock.close()

    def __enter__(self):
        return self

    def __exit__(self, *_exc):
        self.close()


def mcp_call(host, port, token, method, name=None, arguments=None, timeout=10.0):
    """The MCP client: POST one JSON-RPC message and return the decoded reply. The Origin header is
    sent because the server refuses anything but a loopback origin; the Bearer token guards it."""
    params = {"name": name, "arguments": arguments or {}} if name else {}
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode(ENCODING)
    headers = {"Content-Type": "application/json", "Origin": "http://127.0.0.1"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = urllib.request.Request(
        f"http://{host}:{port}/mcp", data=body, headers=headers, method="POST")
    with urllib.request.urlopen(request, timeout=timeout) as reply:
        return json.loads(reply.read().decode(ENCODING))


def tcp_call(host, port, token, name, arguments, timeout):
    """One named call on its own connection: what an MCP tools/call maps to over TCP."""
    with RemoteControl(host, port, token, timeout=timeout) as scope:
        return scope.call(name, **(arguments or {}))


__all__ = ["RemoteControl", "mcp_call", "tcp_call"]
