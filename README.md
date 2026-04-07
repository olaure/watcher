# watcher

A small web server that launches shell scripts on demand and lets clients poll for their output. Includes a CLI for managing tokens, scripts, and configuration.

## System Requirements

- Go >= 1.21
- gcc or clang (CGo required for SQLite)

## Build

```bash
make check-deps   # verify Go and C compiler are available
make build        # produces build/watcher binary
```

## Quick Start

```bash
# 1. Build and initialize (config, directories, database)
make init

# 2. Set up a script with its own role and token in one step
./build/watcher setup deploy /path/to/script.sh arg1 arg2

# 3. Start the server
make renew
```

The `setup` command prints the token and script ID needed to call the API.

## Makefile Targets

| Target | Description |
|---|---|
| `make check-deps` | Verify Go and C compiler are available |
| `make build` | Compile the binary to `build/watcher` |
| `make init` | Build, then initialize config, directories, and database |
| `make renew` | Build, kill running server, start fresh |
| `make kill` | Stop all running watcher server processes |
| `make clean` | Remove the `build/` directory |

## Configuration

Configuration uses three layers (highest priority wins):

1. **DB overrides** — set via `watcher config set`
2. **JSON config file** — `~/.watcher/config.json`
3. **Built-in defaults**

### Config Keys

| Key | Default | Description |
|---|---|---|
| `db_path` | `~/.watcher/watcher.db` | SQLite database location |
| `scripts_dir` | `~/.watcher/scripts` | Default directory for script resolution |
| `logs_dir` | `~/.watcher/logs` | Directory for run log files |
| `listen_addr` | `:8079` | Server listen address |
| `prune_interval` | `60s` | How often the pruner checks for expired logs |
| `enable_api` | `false` | Enable monitoring API endpoints (`/api/*`) |

### Config CLI

```bash
./build/watcher config init              # create config file + directories
./build/watcher config list              # show all resolved values
./build/watcher config get <key>         # show one value and its source
./build/watcher config set <key> <value> # set a DB override
./build/watcher config reset <key>       # remove DB override
```

Global flag `--config <path>` overrides the config file location (default: `~/.watcher/config.json`).

## CLI Reference

### Initialization

```bash
./build/watcher init                     # create config, directories, and database
```

Safe to run repeatedly — skips existing config file.

### Setup (Quick Script + Role + Token)

```bash
./build/watcher setup <name> <script-path> [args...]
./build/watcher setup --watcher <watcher> <name> <remote-script-id>
```

Creates all three resources in one step, sharing the same name:
1. Registers the script (local or proxy) with the given path/remote-id and default args
2. Creates a role with all permissions (`*`) on that script
3. Creates a token assigned to the role

Prints the token value and script ID, ready for use in API calls or CI secrets. Use `--watcher` to create a proxy script linked to a remote watcher.

### Token Management

```bash
./build/watcher token generate --name <name> --role <role>  # generate a new token
./build/watcher token list                                   # list all tokens
./build/watcher token revoke <name>                          # revoke a token
./build/watcher token rename <old-name> <new-name>           # rename a token
./build/watcher token set-role <name> <role>                 # change token role
```

Both `--name` and `--role` are required when generating a token. Token names are unique and used as identifiers in all commands.

### Script Management

```bash
./build/watcher script register <name> <path> [args...]  # register with optional default args
./build/watcher script list                               # list all scripts
./build/watcher script enable <name>                      # enable a script
./build/watcher script disable <name>                     # disable a script
./build/watcher script set-args <name> <args...>          # set default arguments
./build/watcher script clear-args <name>                  # clear default arguments
./build/watcher script set-ttl <name> <duration>          # set log TTL (e.g. 1h, 30m)
./build/watcher script set-cleanup <name> on|off          # toggle auto-cleanup
./build/watcher script rename <old> <new>                 # rename a script
./build/watcher script set-path <name> <path>             # update script path
```

Script paths can be absolute or relative. Relative paths are resolved against `scripts_dir`.

Registered args are prepended to any args provided via the API at launch time. This lets you lock down a script's core parameters while optionally allowing extra args from the caller.

### Role Management

```bash
./build/watcher role create <name> [--parent <name>]    # create a role
./build/watcher role list                                # list roles and permissions
./build/watcher role delete <name>                       # delete a role
./build/watcher role rename <old-name> <new-name>        # rename a role
./build/watcher role set-parent <name> <parent|->        # set parent ('-' to clear)
./build/watcher role grant <role> <script|*> <action>    # grant permission
./build/watcher role revoke <role> <script|*> <action>   # revoke permission
```

Actions: `launch`, `poll`, `all`. Use `*` as the script name for wildcard (all scripts).

### Watcher Management (Federation)

```bash
./build/watcher watcher add <name> <url> <token> [--header K:V]...  # register a remote watcher
./build/watcher watcher list                                         # list all watchers
./build/watcher watcher remove <name>                                # remove a watcher
./build/watcher watcher rename <old-name> <new-name>                 # rename a watcher
./build/watcher watcher set-url <name> <url>                         # update URL
./build/watcher watcher set-token <name> <token>                     # update token
./build/watcher watcher test <name>                                  # test connectivity
./build/watcher watcher link <local-name> <watcher> <remote-id>      # create a proxy script
```

Use `--header` to add custom headers (e.g. Cloudflare Access):

```bash
./build/watcher watcher add prod https://watcher.example.com TOKEN \
  --header CF-Access-Client-Id:xxx --header CF-Access-Client-Secret:yyy
```

### Server

```bash
./build/watcher serve [--config <path>]
```

Or use `make renew` to build, stop any running instance, and start in the background.

## API Reference

All endpoints require an `Authorization: Bearer <token>` header, except `/health`.

### GET /health

Returns `{"status":"ok"}`. No authentication required. Used by `watcher test` for connectivity checks.

### POST /launch

Launch a registered script. If the script has registered args, they are always prepended. Any args in the request are appended after them.

**Request:**
```json
{ "script_id": "abc123..." }
```

With extra arguments (appended after registered args):
```json
{ "script_id": "abc123...", "args": ["extra1", "extra2"] }
```

Arguments are passed to the shell script as positional parameters (`$1`, `$2`, ...). They are passed via the OS argv array, not through shell expansion, so there is no risk of shell injection.

**Response (200):**
```json
{ "run_id": "xyz789...", "status": "running" }
```

**Errors:** `400` (missing script_id), `401` (unauthorized), `403` (insufficient permissions), `404` (script not found or disabled)

### GET /poll?script_id=...&run_id=...

Poll for script output. Each call returns new log data since the last poll (per-token cursor tracking). Both `script_id` and `run_id` are required — the server validates that the run belongs to the given script.

**Response (200) — in progress:**
```json
{
  "run_id": "xyz789...",
  "status": "running",
  "logs": "line 1\nline 2\n",
  "finished": false
}
```

**Response (200) — complete:**
```json
{
  "run_id": "xyz789...",
  "status": "success",
  "logs": "final output\n",
  "finished": true,
  "exit_code": 0
}
```

**Response (200) — logs pruned:**
```json
{
  "run_id": "xyz789...",
  "status": "success",
  "logs": "",
  "finished": true,
  "exit_code": 0,
  "logs_pruned": true
}
```

**Errors:** `400` (missing params), `401` (unauthorized), `403` (insufficient permissions), `404` (run not found or script_id mismatch)

## Role-Based Access Control (RBAC)

Tokens are assigned to roles, and roles define what actions are permitted on which scripts. Permissions are checked on every `/launch` and `/poll` request.

### Seeded Roles

On first run, three roles are created automatically:

| Role | Parent | Permissions | Purpose |
|---|---|---|---|
| `viewer` | — | `poll` on `*` | Can poll all runs but cannot launch |
| `deployer` | `viewer` | (inherits poll) | Inherits poll; grant `launch` per-script |
| `admin` | — | `*` on `*` | Unrestricted access |

Existing tokens (from before RBAC was added) are automatically assigned to `admin`.

### Permission Resolution

Permissions are resolved by walking up the role hierarchy. A request is allowed if any role in the chain (the token's role or any ancestor) has a matching permission. Wildcards (`*`) match any script or action.

**Example**: a `deployer` token with `launch` granted on a specific script can:
- Launch that script (direct grant)
- Poll any run (inherited from `viewer` parent)

### Granting Permissions

```bash
# Let deployer launch a specific script
./build/watcher role grant deployer my-script launch

# Let a custom role do everything on one script
./build/watcher role grant ci-role my-script all
```

## Federation

Federation lets one watcher instance (the "hub") proxy launch and poll requests to remote watcher instances. Clients interact only with the hub — federation is completely transparent.

The hub is a pure proxy: no logs are cached locally. Each poll is forwarded to the remote watcher in real time.

### Push-Based Sync

Remote watchers automatically push their script state to the hub whenever scripts are registered, renamed, enabled, or disabled. No manual linking needed — proxy scripts appear on the hub automatically as `{watcher_name}/{script_name}`.

### Quick Start

```bash
# 1. On the hub: create an invitation
./build/watcher federation invite production
# → prints a hub token

# 2. On the remote: join the hub
./build/watcher federation join https://hub.example.com HUB_TOKEN \
  --url https://remote.example.com

# 3. On the remote: register scripts as usual — they auto-push to the hub
./build/watcher setup deploy /opt/scripts/deploy.sh

# 4. On the hub: proxy scripts appear automatically
curl -H "Authorization: Bearer ADMIN_TOKEN" http://hub:8079/api/scripts
# → shows "production/deploy" proxy script
```

### Federation CLI

```bash
# Hub commands
./build/watcher federation invite <name>                    # create watcher + token, print hub token

# Remote commands
./build/watcher federation join <hub-url> <token> --url <url> [options]  # register with hub
./build/watcher federation leave                            # stop pushing to hub
./build/watcher federation push                             # manual push (retry after downtime)
./build/watcher federation status                           # show federation info
```

Join options:
- `--url <url>` — remote watcher URL (required, used by hub for proxying)
- `--token <token>` — remote admin token for hub to proxy through (auto-generated if omitted)
- `--header K:V` — extra headers for hub requests (repeatable, e.g. Cloudflare Access)

### Manual Watcher Management

For manual federation setup (without push-based sync):

```bash
./build/watcher watcher add <name> <url> <token> [--header K:V]...  # register a remote watcher
./build/watcher watcher list                                         # list all watchers
./build/watcher watcher remove <name>                                # remove a watcher
./build/watcher watcher rename <old-name> <new-name>                 # rename a watcher
./build/watcher watcher set-url <name> <url>                         # update URL
./build/watcher watcher set-token <name> <token>                     # update token
./build/watcher watcher test <name>                                  # test connectivity
./build/watcher watcher link <local-name> <watcher> <remote-id>      # create a proxy script
```

### Permission Model

Federation respects RBAC on both sides:
- The **hub** checks that the caller's token has permission on the local proxy script
- The **remote** checks that the watcher's token has permission on the remote script

This gives you two layers of access control.

## Monitoring API

Four read-only endpoints for dashboard integration. **Disabled by default** — enable with `enable_api: true` in config or `watcher config set enable_api true`.

All `/api/*` endpoints require an admin token (`*` permission on `*`). Returns `403` for non-admin tokens.

### GET /api/overview

Dashboard summary with live watcher health checks (parallelized).

```json
{
  "scripts": { "total": 5, "enabled": 4, "remote": 2 },
  "watchers": { "total": 2, "healthy": 1, "unhealthy": 1 },
  "runs": { "active": 1, "recent_failures": 0 },
  "tokens": { "total": 3, "revoked": 1 }
}
```

### GET /api/scripts

All scripts with last run info.

```json
[
  {
    "id": "abc...", "name": "deploy", "enabled": true, "type": "local",
    "path": "/opt/scripts/deploy.sh", "args": "[]",
    "watcher": null,
    "last_run": { "id": "xyz...", "status": "success", "exit_code": 0,
                  "started_at": "2026-03-30 12:00:00", "finished_at": "2026-03-30 12:01:30" }
  }
]
```

### GET /api/runs?limit=50

Recent runs, newest first. Default limit 50, max 200.

```json
[
  {
    "id": "xyz...", "script_id": "abc...", "script_name": "deploy",
    "status": "success", "exit_code": 0,
    "started_at": "2026-03-30 12:00:00", "finished_at": "2026-03-30 12:01:30"
  }
]
```

### GET /api/watchers

Watchers with live health checks (parallelized, 5s timeout) and linked script count.

```json
[
  {
    "id": "ghi...", "name": "production", "url": "https://prod.example.com",
    "healthy": true, "scripts": 3, "created_at": "2026-03-25 10:00:00"
  }
]
```

### GET /api/watchers/{name}/scripts

Proxies to a remote watcher's `GET /api/scripts`. Fetches the remote's scripts with their last run info on demand. Requires the remote watcher to have `enable_api: true` and the watcher's token to be admin on the remote.

### GET /api/watchers/{name}/runs?limit=50

Proxies to a remote watcher's `GET /api/runs`. The `limit` parameter is forwarded.

## Database Schema

SQLite with WAL mode for concurrent access. Nine tables:

- **config** — key/value overrides for configuration
- **tokens** — auth tokens (id, token, label, role_id, created_at, revoked)
- **scripts** — registered scripts (id, name, path, args, enabled, auto_cleanup, log_ttl_sec, watcher_id, remote_script_id)
- **runs** — script executions (id, script_id, pid, status, exit_code, log_file, logs_pruned, timestamps)
- **poll_cursors** — per-token read position for each run (token_id, run_id, byte_offset)
- **roles** — role definitions (id, name, parent_id, created_at)
- **role_permissions** — permissions per role (role_id, script_id, action)
- **watchers** — remote watcher instances (id, name, url, token, headers, token_id, created_at)
- **federation** — push-based federation config (single-row: hub_url, hub_token, remote_url, remote_token)

## Log Lifecycle

1. **Creation**: when `/launch` is called, a log file is created at `<logs_dir>/<run_id>.log`
2. **Writing**: stdout and stderr from the script are written to the log file in real time
3. **Polling**: `/poll` reads from the file starting at the caller's byte offset, advancing the cursor
4. **Pruning**: for scripts with `auto_cleanup` enabled, the log file is deleted when:
   - The TTL expires after run completion, OR
   - All poll cursors have consumed the entire log
5. **Metadata preserved**: the `runs` row (status, exit_code, timestamps) is kept permanently; only the log file is removed

## Server Logging

The server writes structured logs to `<logs_dir>/server.log` with automatic rotation:

- **Max size**: 10 MB per log file
- **Backups**: 3 rotated files (`server.log.1`, `.2`, `.3`)
- Rotation happens automatically when the log file exceeds the size limit

## Graceful Self-Update

The server supports scripts that restart it (e.g. a deploy script that runs `make renew`). When the server receives SIGTERM:

1. Active runs are marked as "detached" in the database
2. On restart, detached runs are recovered — if the process is still alive, a monitoring goroutine picks it up; otherwise it's marked as successful

This means deploy scripts that include `make renew` will correctly report success to polling clients even though the server was restarted mid-run.

## Project Structure

```
watcher/
  Makefile                 # check-deps, build, init, renew, kill, clean
  main.go                 # subcommand dispatch
  config.default.json     # shipped default config
  cmd/
    init.go               # initialize config, directories, and database
    serve.go              # HTTP server + pruner startup
    token.go              # token generate/list/revoke/rename/set-role
    setup.go              # one-step script + role + token creation
    script.go             # script register/list/enable/disable/set-ttl/set-cleanup/rename/set-path
    role.go               # role create/list/delete/rename/set-parent/grant/revoke
    watcher.go            # watcher add/list/remove/rename/set-url/set-token/test/link
    federation.go         # federation invite/join/leave/push/status + HubClient + pushToHub
    config.go             # config init/get/set/reset/list
    logging.go            # rotating log writer
  config/
    config.go             # layered config resolution
  db/
    db.go                 # SQLite connection + WAL setup + RBAC migration
    migrations.go         # schema DDL
  api/
    middleware.go          # Bearer token auth + RBAC permission check
    launch.go             # POST /launch handler
    poll.go               # GET /poll handler
    routes.go             # mux wiring + health endpoint + JSON helpers
    proxy.go              # HTTP client for remote watchers (hub→remote proxy)
    federation.go         # POST /federation/sync handler (remote→hub push)
    monitor.go            # monitoring API handlers (/api/*)
  runner/
    runner.go             # script execution + log capture + process recovery
    pruner.go             # periodic log file cleanup
  id/
    id.go                 # base64url GUID generation (128-bit)
  scripts/
    pull_and_build.sh     # example script: git pull + make build + make renew
```

## IDs

All IDs (tokens, scripts, runs) are 22-character base64url-encoded strings generated from 16 random bytes (128 bits of entropy, equivalent to UUIDs but shorter).
