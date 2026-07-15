# Remote Control PR - architecture overview

This is the short, plain-text explanation. Operational usage is in
[REMOTE_CONTROL_REFERENCE.md](REMOTE_CONTROL_REFERENCE.md).

## The architecture

```text
  [ TCP client ]
        |
        | public TCP
        +-----------------------------------------------------+
                                                              |
                                                              v
  [ MCP client ]                                    [ SAME Core-side TCP server ]
        |                                                     |
        | MCP                                                 |
        v                                                     |
  [ HTTP endpoint ]                                           |
        |                                                     |
        v                                                     |
  [ MCP helper ]                                              |
        |                                                     |
        | private loopback TCP                                |
        +-----------------------------------------------------+
                                                              |
                                                              v
                                                  [ ONE BOTTLENECK ]
                                                              |
                                         1. Is the request well formed?
                                         2. Is the call allowed?
                                         3. Are values and limits safe?
                                         4. Is the system free?
                                                              |
                                                   +----------+----------+
                                                   |                     |
                                                 valid                 invalid
                                                   |                     |
                                                   v                     v
                                         [ Run named handler ]        [ Error ]
                                                   |
                                                   v
                                         [ Existing mesoSPIM Core ]
```

Both routes use the same TCP server, validator, busy gate, and command handlers. MCP
does not have a separate microscope-control implementation.

## Why MCP -> HTTP -> TCP?

MCP lets compatible clients discover and call named tools. HTTP is the most widely
supported connection layer. TCP provides a small server close to the mesoSPIM `Core`.

The MCP helper therefore converts each microscope `tools/call` into a private TCP call.
This deliberately sends MCP and direct TCP through one bottleneck, so they cannot apply
different validation rules. MCP/HTTP runs in a separate process, outside the Core
thread.

```text
  Direct TCP --------------------------+
                                       |
  MCP -> HTTP -> private TCP ----------+
                                       |
                                       v
       TCP server -> parse -> validate -> busy gate -> handler -> Core
```

## What happens when the server starts?

Before opening its TCP socket, the server tests the real validation path against an
in-memory `SimCore`. No hardware is moved.

The self-test accepts in-range moves and rejects values above and below every loaded
axis limit. It also rejects an invalid intensity, an unknown axis, and an unknown
command. It verifies that only valid moves reached `SimCore`. If any check fails, or no
usable stage limit exists, startup fails and no server is exposed.

In TCP mode, the server then binds to the configured public port. In MCP mode, it first
binds a private loopback TCP port with a random internal password, then starts the
public MCP/HTTP helper.

## What happens when a client connects?

A TCP client must send valid bounded frames and the correct password. A malformed frame
is rejected. A wrong password returns `AUTH-FAILED` and closes the connection.

An MCP request must use `POST /mcp`, an accepted local Origin when Origin is present,
one correct Bearer token, a valid content length, a body no larger than 1 MiB, and
strict JSON. A valid microscope `tools/call` is then forwarded through the private TCP
connection and follows the same path as direct TCP.

## What does the shared bottleneck check?

```text
  1. REQUEST STRUCTURE
     The frame and JSON are valid. There is exactly one command name and its
     arguments form an object. Duplicate fields, NaN, and Infinity are rejected.

                           |
                           v

  2. ALLOWED CALL
     The command name exists in the explicit COMMANDS allowlist. Clients cannot
     execute arbitrary Python or automatically access other Core methods.

                           |
                           v

  3. VALUES AND LIMITS
     Types, required fields, device options, numeric ranges, acquisition fields,
     snapshot bounds, and stage movement limits are checked before Core is called.

     Absolute move:  min <= requested target <= max

     Relative move:  min <= current position + requested delta <= max

                           |
                           v

  4. BUSY STATE
     Read-only calls remain available. Only one mutation may be active across TCP
     and MCP. A conflicting mutation is rejected before its handler runs.
```

Stage limits come from the loaded mesoSPIM configuration, with optional operator
overrides. Remote clients can read the effective limits but cannot change them.

## What happens after validation?

A valid request runs its named handler, which calls the existing mesoSPIM `Core` API.
Read-only calls return immediately. Mutations receive an operation ID. Long operations
remain busy until mesoSPIM reports real completion; the server does not guess a
completion time.

```text
  valid request -> named handler -> existing Core method

  mutation -> processing -> real completion event -> completed
```

`get_info` remains lightweight. Snapshot pixels are transferred separately through
bounded `get_snap_image` chunks. Existing mesoSPIM warning dialogs are unchanged.

## Where the code lives

Three self-contained modules. The two files mesoSPIM already owns gain six lines between
them, so the contribution is easy to review and easy to remove.

```text
  mesoSPIM_RemoteControl_ValidateAndRunCommands.py   the allowlist, _validate, run(), the handlers
  mesoSPIM_RemoteControl_Servers.py                  the TCP server and the MCP/HTTP helper
  mesoSPIM_RemoteControl_Tab.py                      the GUI tab: its widgets, settings, MCP child

  mesoSPIM_Core.py         a declared _remote_session + _remote_control_server, and two slots
  mesoSPIM_MainWindow.py   an import, a construction, a shutdown call
```

The tab follows the convention mesoSPIM already uses for its other optional features
(`mesoSPIM_Optimizer`, `ProcessorChainWindow`): the component owns its widgets and state,
and MainWindow holds only a handle.

To add a command, see the module docstring of
`mesoSPIM_RemoteControl_ValidateAndRunCommands.py` — it is a four-step recipe.

## Who owns the operation state, and why

The busy gate, the operation counter, the pending snapshot and the operator's saved
acquisition list live in **one declared attribute on Core**, `_remote_session` — not on the
server.

That is deliberate. Stopping and starting the server is a single click in the GUI. If the
session belonged to the server, a restart during a long acquisition would wipe the busy gate,
and a remote client could then drive `move_absolute` into hardware that is still running.
Core-owned state survives the restart and keeps the gate fail-closed.

It is created eagerly when Core is constructed, never lazily. Two threads reach for it — see
below — and lazy creation would let both build one, losing the operation that was in flight.

## Threading

```text
  GUI thread      the tab; emits start/stop to Core through a QUEUED connection
  Core thread     the TCP server is constructed here, and dispatches every command here
  camera thread   the camera-frame signal fires here; capture_snap_image() runs here
```

`RemoteControlTCPServer` is a plain Python object, not a `QObject`. That is load-bearing: the
camera-frame connection therefore stays direct, and `capture_snap_image` reads
`frame_queue_display` — a `deque(maxlen=1)` — with no queued delay. Making the server a
`QObject` without also changing the image handoff would insert latency in front of a
single-slot buffer, which is a frame-substitution race.

So the camera thread *writes* the snapshot into the session while the Core thread mutates the
operation. That cross-thread access is the current, live-validated behaviour. A future
improvement would capture the image bytes on the camera thread and queue the result to Core.

## Known limitation: a snap whose frame never arrives

A remote `snap` completes only when the camera frame signal fires. If it never does, the
operation stays active, and every mutating command is refused with `BusyError` until mesoSPIM
is restarted.

Stopping and starting the server does **not** clear it — that is the point of Core-owned state
above, and it is not a recovery path. Operation-ID matching would not help either: it prevents
a stale callback from completing a *newer* operation, and does nothing about a completion
signal that never arrives. Real recovery needs a timeout, a cancel-on-stop transition, or an
explicit recovery command. None of those are in this contribution.
