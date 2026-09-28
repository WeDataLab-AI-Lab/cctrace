# Sync daemon

The sync daemon uploads session logs from disk to the server's sync endpoint. OTEL telemetry does not go through it; the agents send that themselves.

## How it starts

With session log sync enabled, `cctrace init` installs two Claude Code hooks (see [Claude Code](../agents/claude-code.md)):

| Hook | Runs | Effect |
|---|---|---|
| `SessionStart` | `cctrace sync --daemon ... --auto-profile --interval 1s` | Starts a background watch process for the profile, polling every second. If one is already running, the new one exits. |
| `SessionEnd` | `cctrace sync --daemon --once ... --auto-profile` | Runs one extra pass in the background. The hook is asynchronous and does not stop the running daemon, which keeps serving other open sessions. |

One daemon per profile collects every agent enabled in that profile: Claude Code, and Codex CLI, GJC, or OMO when their options are on. Codex, GJC, and OMO install no hook of their own. If you use only those agents, start the daemon yourself:

```bash
cctrace sync --daemon
```

The daemon prints its pid, its log path, and `Stop: cctrace sync --stop`.

## Run a sync by hand

```bash
cctrace sync                 # one pass, then exit
cctrace sync --watch         # stay in the foreground, one pass every 30s
cctrace sync --dry-run       # list new records per file without sending
```

`cctrace sync` with no profile, directory, or mode flags runs one pass for the default profile and then for each named profile.

## Flags

| Flag | Default | Effect |
|---|---|---|
| `--watch` | off | Keep running in the foreground and poll for new records |
| `--interval <duration>` | `30s` | Poll interval for watch mode, as a Go duration (`30s`, `2m`). The installed hook uses `1s`. |
| `--daemon` | off | Start a background watch process and return |
| `--once` | off | With `--daemon`: run one pass in the background and exit. Used by the `SessionEnd` hook. |
| `--stop` | off | Ask the running daemon to finish its pass and exit. Waits up to 35 seconds. |
| `--dry-run` | off | Show what would be sent, including which Codex, GJC, and OMO homes would be scanned. Sends nothing. |
| `--claude-dir <dir>` | profile's Claude home, else `~/.claude` | Claude config directory to read session logs from |
| `--profile <name>` | `CCTRACE_PROFILE`, else default profile | Named profile to use |
| `--auto-profile` | off | Pick the named profile whose Claude home matches `--claude-dir`, or `CLAUDE_CONFIG_DIR` when that flag is absent |
| `--profile-email <email>` | `CCTRACE_PROFILE_EMAIL`, else the profile's email | Email attached to uploaded records |
| `--endpoint <url>` | profile's sync endpoint | Send to this HTTP endpoint instead, for example `http://localhost:8080` |
| `--local` | off | Development use: send to a server on localhost. Reads `CCTRACE_LOCAL_SYNC_ENDPOINT` and `CCTRACE_LOCAL_OTEL_ENDPOINT`, or `HTTP_PORT` and `GRPC_PORT`, from the environment or the nearest .env file in or above the current directory; otherwise uses ports 8080 and 4317. |

The upload endpoint is chosen in this order: `--endpoint`, then the profile's sync endpoint, then its OTEL endpoint.

The hooks also pass a hidden `--log-to-file` flag, which sends the process's own output to the profile's log. You do not need it when running `sync` by hand.

## What gets uploaded

- Claude Code session logs under the Claude home's `projects` directory.
- Codex CLI, GJC, and OMO session files, when enabled for the profile. See [Codex CLI](../agents/codex.md) and [GJC and OMO](../agents/gjc-omo.md).

The first pass for each agent skips what is already on disk: every existing file is marked as read up to its current end, and only content written after that is uploaded. History from before the install is therefore not uploaded. The exception is the Claude Code session whose `SessionStart` hook started the daemon: it is read from the beginning. `cctrace status` reports how many files were skipped this way.

On every start, the daemon also compares the Claude settings file with what the current binary would write, and rewrites it if they differ.

When uploads fail, the daemon backs off before retrying (about 5 minutes at most, longer if the server asks for it). If sending has failed for 30 minutes because the server cannot be reached, and the `SessionStart` hook is installed, the daemon releases its lock and exits so that the next session starts a fresh one. Read offsets are kept, so nothing is lost.

## Logs and state files

Each profile keeps these files in its profile directory: `~/.cctrace/` for the default profile, `~/.cctrace/profiles/<name>/` for a named one.

| File | Contents |
|---|---|
| `~/.cctrace/sync.log` | Daemon log. Rotates at 10 MB and keeps 5 old files. |
| `~/.cctrace/sync-crash.log` | Raw output of the background process, such as panics. Trimmed when it passes 1 MB. |
| `~/.cctrace/sync-state.json` | How far each Claude Code session file has been read |
| `~/.cctrace/codex-sync-state.json`, `~/.cctrace/gjc-sync-state.json`, `~/.cctrace/omo-sync-state.json` | The same for Codex, GJC, and OMO |
| `~/.cctrace/sync.lock`, `~/.cctrace/sync.pid`, `~/.cctrace/sync-runtime.json` | Single-instance lock and the running daemon's pid and version |

Keep the state files. A missing state file counts as a first run, so the next pass skips the existing content of every file instead of uploading it.

## Stop the daemon

```bash
cctrace sync --stop
```

`--stop` asks the daemon to finish and waits for it. It fails with `no running daemon found` when nothing is running. The next Claude Code session starts a new daemon through the hook.

If the daemon does not respond, force it:

```bash
cctrace kill
```

`kill` terminates the process holding the profile's sync lock, waits up to 5 seconds for the lock to be released, and clears the runtime files. When no daemon holds the lock it prints `No running sync daemon` and succeeds. It takes `--profile` and reads `CCTRACE_PROFILE`.

Both commands act on one profile. Add `--profile <name>` for a named one.

## Upgrading the client

Clients built from this repository never replace themselves. A server built as described in [Server install](../server/install.md) reports version `dev`, and the client then skips the update check. If the server reports a newer release version, the attempt fails because the client has an empty update public key, and `~/.cctrace/sync.log` records the reason on an `update:` line, for example `update: update public key is not configured`. Collection continues on the current binary either way.

To upgrade:

1. Rebuild the client (see [Install](install.md)) and copy it over the installed binary.
2. If the new binary is at a different path, run `cctrace env apply` from it so that the hooks point at it.
3. Restart the daemon so that it runs the new code: `cctrace sync --stop`, then `cctrace sync --daemon` or start a new Claude Code session.

While an older daemon is still running, `cctrace status` shows a notice under `수집 상태` naming both versions.
