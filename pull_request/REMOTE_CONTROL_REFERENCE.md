# Remote Control call reference

TCP and MCP expose the same 53 commands. They use the same names, arguments, limits, operation
state, and error codes. The operator chooses one transport in the Remote Control tab; both cannot
run together.

Call `get_manual`, `get_info`, and `get_limits` before making changes. `get_manual` returns this same
accepted/rejected-and-poll workflow over both transports, together with a command list generated
from the running command registry.

## Connect

### TCP

The default address is `127.0.0.1:42000`. TCP messages use this frame format:

```text
<number of UTF-8 bytes>\n<payload>
```

Send the password as the first frame. The server replies `OK` or `AUTH-FAILED`. After that, send one
JSON command per frame. A successful reply begins with `__MESOSPIM_OK__`; an error begins with
`error: [code]`.

### MCP

The default URL is `http://127.0.0.1:42100/mcp`. Send JSON-RPC POST requests with:

```text
Authorization: Bearer <Remote Control password>
```

Microscope commands use the MCP method `tools/call`. `initialize` and `tools/list` are also
supported. The endpoint advertises MCP revision `2024-11-05` and intentionally implements only
these three methods as authenticated HTTP POST requests. It does not claim the resources, prompts,
streaming, sessions, or complete lifecycle of newer MCP Streamable HTTP revisions.

The host and ports can be changed in the tab. Do not hard-code or commit the password.

## Call format

TCP payload:

```json
{"move_absolute":{"targets":{"x":100}}}
```

Equivalent MCP request:

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"move_absolute","arguments":{"targets":{"x":100}}}}
```

The server executes only names from its fixed command list. It never executes Python or text sent by
the client.

## Accepted calls and polling

Read commands return their data directly and do not create an operation. Every ordinary mutation
is either rejected before it starts or accepted with a new operation record. Rejection checks
include the command name, argument shape, value types, configured options, hardware and stage
limits, and the one-mutation gate.

```json
{
  "accepted": true,
  "accepted_command": "move_absolute",
  "operation": {
    "id": "op-000123",
    "command": "move_absolute",
    "status": "processing"
  }
}
```

For an ordinary mutation, `accepted` means the request passed validation, was admitted by the gate,
and was scheduled. Core, hardware, and GUI work begins afterward. It does not mean the requested
work has finished.

When the status is `processing` or `stopping`, call `get_progress` through the same TCP or MCP
transport and verify that the returned operation ID is unchanged. Stop when its status becomes
`completed` or `failed`. On completion, command-specific output is stored in `operation.result`.
On failure, the reason is stored in `operation.error`.

Emergency commands validate and execute immediately so they remain available while the ordinary
mutation gate is busy. They do not create a new operation. Their reply contains the current
operation snapshot; if it has an ID, continue polling that operation until it is terminal.

For stage movement, the command is sent with `wait_until_done=False`. mesoSPIM remains able to answer
reads while the stage travels. The operation becomes `completed` only when position readback reaches
every requested axis within the configured tolerance. The operation reports both `target` and the
latest `observed` position.

Important client rules:

- Never resend accepted ordinary work because polling is slow.
- If a response is lost, reconnect and inspect `get_progress` before deciding what happened.
- A synchronous acquisition preflight can temporarily delay TCP reads. Retrying the read-only
  `get_progress` call is safe; retrying the accepted mutation is not.
- Match the operation ID; `get_progress` reports the latest operation, not an operation history.
- A second mutation is rejected with `busy` while one is still running.
- Reads and emergency commands remain available while an asynchronous mutation is active, subject
  to the Core event loop being responsive.

## Small TCP client example

This example uses only the Python standard library:

```python
import json
import os
import socket
import time


def send_frame(sock, text):
    data = text.encode("utf-8")
    sock.sendall(str(len(data)).encode("ascii") + b"\n" + data)


def receive_frame(sock):
    data = b""
    while b"\n" not in data:
        data += sock.recv(4096)
    header, _, payload = data.partition(b"\n")
    size = int(header)
    while len(payload) < size:
        payload += sock.recv(4096)
    return payload[:size].decode("utf-8")


def call(sock, name, **arguments):
    send_frame(sock, json.dumps({name: arguments}))
    reply = receive_frame(sock)
    if not reply.startswith("__MESOSPIM_OK__"):
        raise RuntimeError(reply)
    return json.loads(reply[len("__MESOSPIM_OK__") :])


def wait_for_operation(sock, accepted):
    operation_id = accepted["operation"]["id"]
    while True:
        operation = call(sock, "get_progress")["operation"]
        if operation.get("id") != operation_id:
            raise RuntimeError("the latest operation changed")
        if operation.get("status") == "completed":
            return operation
        if operation.get("status") == "failed":
            raise RuntimeError(operation.get("error", "operation failed"))
        time.sleep(0.05)


host = os.environ.get("MESOSPIM_REMOTE_HOST", "127.0.0.1")
port = int(os.environ.get("MESOSPIM_REMOTE_PORT", "42000"))
timeout = float(os.environ.get("MESOSPIM_REMOTE_TIMEOUT", "10"))
with socket.create_connection((host, port), timeout=timeout) as sock:
    send_frame(sock, os.environ["MESOSPIM_REMOTE_PASSWORD"])
    assert receive_frame(sock) == "OK"
    print(call(sock, "get_info"))
    accepted = call(sock, "move_absolute", targets={"x": 100})
    print(wait_for_operation(sock, accepted))
```

## Error codes

Both transports return the same code and a readable message.

| Code | Meaning | Client action |
| --- | --- | --- |
| `validation` | A type, option, range, limit, or argument name is wrong. Nothing started. | Correct the request. |
| `busy` | Another mutation is running. | Keep polling the active operation, then try again. |
| `unknown_command` | The command name is not supported. | Correct the name. |
| `execution` | Dispatch or an immediate read/emergency handler failed. | Read the message; poll any returned operation ID before retrying. |

## Values used in the table

Use current values from these read commands when building a request:

- `STATE`: `get_state`
- `POS`: `get_position`
- `ALL`: `get_state_all`
- `INFO`: `get_info`
- `ACQ`: one valid acquisition with current settings and a writable output folder

For example, `STATE.filter` means the returned filter value, not the literal text
`STATE.filter`. Confirm that `get_limits` reports `stage_type: DemoStage` before development or
adversarial tests.

`z_start`, `z_end`, and `z_step` determine the actual acquisition image count. `planes` is retained
as mesoSPIM table metadata and is preserved exactly during list round trips.

## Commands

The arguments column gives one valid request shape. A command may also accept optional fields or
state-derived defaults; use its `get_manual` hint and `get_capabilities` values for the running
microscope.

| Command | Arguments | Purpose |
| --- | --- | --- |
| `hello` | `{}` | Return application, version, protocol, and state. |
| `ping` | `{}` | Confirm that the server answers. |
| `get_state` | `{}` | Return the main microscope settings. |
| `get_position` | `{}` | Return `x`, `y`, `z`, `f`, and `theta`. |
| `get_state_all` | `{"keys":["state"]}` or `{}` | Return selected or all state fields. |
| `get_config` | `{}` | Return configured lasers, filters, zooms, axes, and camera size. |
| `get_info` | `{}` | Return current state, stage type, paths, ETL file, and latest operation. |
| `get_limits` | `{}` | Return active movement and setting limits. |
| `get_capabilities` | `{}` | Return supported commands, axes, modes, and fields. |
| `get_manual` | `{}` | Return the built-in workflow and command guide. Call this first. |
| `get_progress` | `{}` | Return acquisition progress and the latest operation. |
| `self_test` | `{}` | Test limits against a simulated Core. It never moves hardware. |
| `move_absolute` | `{"targets":{"x":POS.x}}` | Schedule an in-limit absolute move and confirm it by readback. |
| `move_relative` | `{"deltas":{"x":0}}` | Schedule an in-limit relative move and confirm its calculated target. |
| `zero` | `{"axes":["x"]}` | Define the current X position as zero. |
| `unzero` | `{"axes":["x"]}` | Restore physical X coordinates after `zero`. |
| `stop` | `{}` | Stop stage movement. |
| `stop_activity` | `{}` | Stop live or acquisition activity. |
| `clear_stuck_operation` | `{}` | Release a lost-completion operation only after independent state proves it is safe. It does not abort active work. |
| `set_state` | `{"settings":{"intensity":STATE.intensity}}` | Change supported state fields. |
| `set_filter` | `{"filter":STATE.filter,"wait":true}` | Select a configured filter. |
| `set_zoom` | `{"zoom":STATE.zoom,"wait":true,"update_etl":false}` | Select a configured zoom. |
| `set_laser` | `{"laser":STATE.laser,"wait":true,"update_etl":false}` | Select a configured laser. |
| `set_intensity` | `{"intensity":STATE.intensity,"wait":true}` | Set intensity from 0 to 100. |
| `set_shutterconfig` | `{"shutterconfig":STATE.shutterconfig}` | Select a configured shutter arrangement. |
| `set_camera` | `{"camera_exposure_time":ALL.camera_exposure_time}` | Change supported camera settings. |
| `set_etl` | `{"etl_l_amplitude":ALL.etl_l_amplitude}` | Change supported ETL settings. |
| `set_galvo` | `{"galvo_l_frequency":ALL.galvo_l_frequency}` | Change supported galvo settings. |
| `set_laser_timing` | `{"laser_l_delay_%":ALL.laser_l_delay_%}` | Change supported laser timing settings. |
| `reload_etl_config` | `{"path":INFO.etl_config_path,"wait":true}` | Reload the current ETL file. |
| `update_etl_from_laser` | `{"laser":STATE.laser,"wait":true}` | Load ETL values for the selected laser. |
| `update_etl_from_zoom` | `{"zoom":STATE.zoom,"wait":true}` | Load ETL values for the selected zoom. |
| `open_shutters` | `{}` | Open the shutters. |
| `close_shutters` | `{}` | Close the shutters, including while another operation is busy. |
| `start_live` | `{}` | Start live mode; end it with `stop_activity`. |
| `start_visual_mode` | `{}` | Start visual mode; end it with `stop_activity`. |
| `start_lightsheet_alignment_mode` | `{}` | Start alignment mode; end it with `stop_activity`. |
| `load_sample` | `{}` | Schedule movement to the configured sample-load position. |
| `unload_sample` | `{}` | Schedule movement to the configured sample-unload position. |
| `center_sample` | `{}` | Schedule movement to the configured sample-center position. |
| `save_etl_config` | `{}` | Save ETL settings to the current file. |
| `get_acquisition_list` | `{}` | Return the current acquisition list. |
| `set_acquisition_list` | `{"acquisitions":[ACQ],"selected_row":0}` | Install a validated non-empty list in Core and the visible table. |
| `run_acquisition_list` | `{}` | Run the installed acquisition list. |
| `run_selected_acquisition` | `{"row":0}` | Run the selected list row. |
| `preview_acquisition` | `{"row":0,"z_update":true}` | Preview the selected row. |
| `acquire_start` | `{"acquisition":ACQ}` | Start one supplied acquisition. |
| `stat_files` | `{"files":[]}` | Report missing files and sizes of existing files. |
| `acquire_finish` | `{}` | Restore the list saved by `acquire_start`. |
| `get_disk_space` | `{"acquisitions":[ACQ]}` | Report free and required disk space. |
| `check_motion_limits` | `{"acquisitions":[ACQ]}` | Report acquisition positions outside limits. |
| `time_lapse_start` | `{"timepoints":1,"interval_sec":0}` | Start a time lapse using the installed list. |
| `time_lapse_stop` | `{}` | Stop the time-lapse schedule. |

## Limit enforcement

The server validates structure, names, types, configured options, numeric ranges, stage travel, and
the active operation before calling mesoSPIM.

Before opening a network port, startup runs the same movement checks against a simulated Core using
the loaded microscope configuration. If any axis lacks a usable limit or the check fails, the
server does not start. `self_test` repeats this hardware-free check on demand.
