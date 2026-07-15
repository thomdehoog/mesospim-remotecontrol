"""The TCP and MCP clients the live tests drive the microscope through.

They come from the module under test, not from a copy, so the tests exercise the very client
an operator imports and the wire format cannot drift between the two.
"""

from tests.support.patch_loader import srv

RemoteControl = srv.RemoteControl
mcp_call = srv.mcp_call

__all__ = ["RemoteControl", "mcp_call"]
