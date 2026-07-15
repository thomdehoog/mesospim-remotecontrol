# TCP and MCP call reference

The current implementation exposes **55** calls. TCP and MCP use the same call name,
arguments, validation, busy gate, and result.

## Connect

**TCP:** connect to `127.0.0.1:42000`, then send the password as the first
length-prefixed frame. A frame is `<UTF-8 byte count>\n<payload>`. Authentication returns
`OK` or `AUTH-FAILED`.

**MCP:** POST JSON-RPC to `http://127.0.0.1:42100/mcp` with
`Authorization: Bearer <password>`. MCP microscope calls use `tools/call`.

The host and ports may be changed in the Remote Control tab. Never store the password in
this file or client code.

### Connect to TCP from Python

You do not implement the wire format. The client ships with the server, and it is the same
one the MCP bridge and the test suite use — so it cannot drift from the server that speaks
to it. Import it wherever the `mesoSPIM` package is on the path; everything at module level
there is standard library, so this pulls in no Qt and no hardware driver.

```python
import os

from mesoSPIM.src.mesoSPIM_RemoteControl_Servers import RemoteControl

scope = RemoteControl(
    host=os.environ.get("MESOSPIM_REMOTE_HOST", "127.0.0.1"),
    port=int(os.environ.get("MESOSPIM_REMOTE_PORT", "42000")),
    token=os.environ["MESOSPIM_REMOTE_PASSWORD"],  # never hard-code the password
)
try:
    print(scope.call("get_info"))
    scope.call("move_absolute", targets={"x": 100})
finally:
    scope.close()
```

`call()` returns the decoded result dictionary, and raises on connection, authentication,
validation or execution failure — the server's error text is raised verbatim. Take the call
name and arguments from the table below; **you never send Python code to the server.**

For a single call on its own connection, `tcp_call(host, port, token, name, arguments,
timeout)` does connect-authenticate-call-close in one step. That is what the MCP bridge
makes per `tools/call`.

### Connect an MCP client

Start MCP in the Remote Control tab, then configure the client with:

```text
MCP URL: http://<host>:<port>/mcp
Bearer token: <Remote Control password>
Call get_info and get_limits before making changes.
Poll get_progress until each operation reports completed or failed.
```

## Call format

For every row in the table, substitute its call name and arguments into one wrapper.

**TCP payload**

```json
{"CALL_NAME": {"argument": "value"}}
```

**MCP request**

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"CALL_NAME","arguments":{"argument":"value"}}}
```

TCP success is `__MESOSPIM_OK__` followed by JSON. MCP success is JSON text in
`result.content[0].text`; parse that text as JSON. MCP sets `isError: true` when the
shared command path rejects the call.

## Values used below

Read these first so the examples match the loaded microscope configuration:

- `STATE` = result of `get_state`
- `POS` = result of `get_position`
- `ALL` = result of `get_state_all`
- `INFO` = result of `get_info`
- `ACQ` = one valid acquisition using current positions/settings and a writable folder

Names such as `STATE.filter` mean: insert that returned JSON value, not the literal text.
Using current values makes the setting examples valid and minimally disruptive. Confirm
`INFO.stage_type == "DemoStage"` before development or adversarial mutations.

## All calls

| Call | Arguments object | What it does |
|---|---|---|
| `hello` | `{}` | Returns application, version, protocol, and state |
| `ping` | `{}` | Confirms the server responds |
| `get_state` | `{}` | Returns the main microscope settings |
| `get_position` | `{}` | Returns `x`, `y`, `z`, `f`, and `theta` |
| `get_state_all` | `{}` | Returns all state fields |
| `get_config` | `{}` | Returns configured lasers, filters, zooms, axes, and camera size |
| `get_info` | `{}` | Returns fresh state, stage type, paths, ETL file, and operation |
| `get_limits` | `{}` | Returns effective movement and parameter limits |
| `get_capabilities` | `{}` | Returns calls, axes, modes, and supported fields |
| `get_progress` | `{}` | Returns progress and latest operation status |
| `get_snap_image` | `{"operation_id": SNAP_ID, "offset": 0}` | Reads a completed remote snapshot; call `snap` first |
| `self_test` | `{}` | Tests limit enforcement against `SimCore`, never hardware |
| `move_absolute` | `{"targets":{"x": POS.x}}` | Moves to an in-limit absolute position |
| `move_relative` | `{"deltas":{"x": 0}}` | Makes an in-limit relative move; zero is a safe example |
| `zero` | `{"axes":["x"]}` | Defines current X as zero |
| `unzero` | `{"axes":["x"]}` | Restores X physical coordinates after `zero` |
| `stop` | `{}` | Stops stage movement |
| `stop_activity` | `{}` | Stops live or acquisition activity |
| `set_state` | `{"settings":{"intensity": STATE.intensity}}` | Changes allowlisted state fields |
| `set_filter` | `{"filter": STATE.filter, "wait": true}` | Selects a configured filter |
| `set_zoom` | `{"zoom": STATE.zoom, "wait": true, "update_etl": false}` | Selects a configured zoom |
| `set_laser` | `{"laser": STATE.laser, "wait": true, "update_etl": false}` | Selects a configured laser |
| `set_intensity` | `{"intensity": STATE.intensity, "wait": true}` | Sets intensity from 0 to 100 |
| `set_shutterconfig` | `{"shutterconfig": STATE.shutterconfig}` | Selects a configured shutter arrangement |
| `set_camera` | `{"camera_exposure_time": ALL.camera_exposure_time}` | Changes allowlisted camera settings |
| `set_etl` | `{"etl_l_amplitude": ALL.etl_l_amplitude}` | Changes ETL settings |
| `set_galvo` | `{"galvo_l_frequency": ALL.galvo_l_frequency}` | Changes galvo settings |
| `set_laser_timing` | `{"laser_l_delay_%": ALL.laser_l_delay_%}` | Changes laser timing settings |
| `reload_etl_config` | `{"path": INFO.etl_config_path, "wait": true}` | Reloads the current ETL file |
| `update_etl_from_laser` | `{"laser": STATE.laser, "wait": true}` | Loads ETL values for the current laser |
| `update_etl_from_zoom` | `{"zoom": STATE.zoom, "wait": true}` | Loads ETL values for the current zoom |
| `open_shutters` | `{}` | Opens the shutters |
| `close_shutters` | `{}` | Closes the shutters, including while busy |
| `snap` | `{}` | Captures one remote snapshot without writing it locally |
| `set_mode` | `{"mode":"idle"}` | Requests a named mode; idle is the safe example |
| `start_live` | `{}` | Starts live mode; finish with `stop_activity` |
| `start_visual_mode` | `{}` | Starts visual mode; finish with `stop_activity` |
| `start_lightsheet_alignment_mode` | `{}` | Starts alignment mode; finish with `stop_activity` |
| `load_sample` | `{}` | Moves to the configured sample-load position |
| `unload_sample` | `{}` | Moves to the configured sample-unload position |
| `center_sample` | `{}` | Moves to the configured sample-center position |
| `execute_stage_program` | `{}` | Runs the configured stage-controller program |
| `save_etl_config` | `{}` | Saves ETL settings to the current ETL file |
| `get_acquisition_list` | `{}` | Returns the acquisition list |
| `set_acquisition_list` | `{"acquisitions":[ACQ], "selected_row":0}` | Installs one valid acquisition |
| `run_acquisition_list` | `{}` | Runs the installed acquisition list |
| `run_selected_acquisition` | `{"row":0}` | Runs row zero of the installed list |
| `preview_acquisition` | `{"row":0, "z_update":true}` | Previews row zero of the installed list |
| `acquire_start` | `{"acquisition":ACQ}` | Starts one supplied acquisition |
| `stat_files` | `{"files":[]}` | Reports missing files and existing file sizes |
| `acquire_finish` | `{}` | Restores adapter state after `acquire_start` |
| `get_disk_space` | `{"acquisitions":[ACQ]}` | Reports free and required disk space |
| `check_motion_limits` | `{"acquisitions":[ACQ]}` | Reports acquisition positions outside limits |
| `time_lapse_start` | `{"timepoints":1, "interval_sec":0}` | Starts one timelapse point; requires an acquisition list |
| `time_lapse_stop` | `{}` | Stops the timelapse schedule |

## Completion and safety

For mutations, poll `get_progress` until the returned operation reports `completed` or
`failed`. Do not use elapsed time as proof of completion. While busy, reads remain
available and conflicting TCP or MCP mutations are rejected.

For `get_snap_image`, use the completed `snap` operation ID as `SNAP_ID`, then continue
with each returned `next_offset` until `complete` is true.

The server checks request structure, the call allowlist, argument types/options/ranges,
absolute and relative stage limits, and the shared busy state before running a handler.

## Why you can trust the limits

You do not have to check that the limits are enforced: **a server that failed to enforce them
would not be running.** Before it binds a socket, it runs `self_test` against a mock Core
carrying this instrument's real config — accepting an in-range move, refusing one past every
axis limit, refusing a bad option and an unknown command — and it **refuses to start** if any
of that fails. A drifted limits file therefore takes the server offline; it never exposes the
instrument.

You can re-run that same check over the wire at any time with the `self_test` call.
