# CLI

Commands of the `cctrace` client that are covered by this guide. `cctrace --help` and `cctrace <command> --help` list every flag.

## Setup and configuration

| Command | Flags | Does |
|---|---|---|
| `cctrace init` | `--profile <name>` | Authenticate against the server, write the profile and the agent settings. See [Connect](../client/setup.md). |
| `cctrace status` | `--profile <name>` | Show identity, endpoints, collection state, paths, options, and quota. Exit code 1 without a profile, 2 when the OTEL endpoint is unreachable. |
| `cctrace env apply` | `--profile <name>`, `--all` | Rewrite the OTEL variables and hooks in the Claude settings file from the saved profile, using the current binary's path |
| `cctrace config list` | `--profile <name>` | List profile settings, tokens masked. `cctrace config` alone does the same. |
| `cctrace config get <key>` | `--profile <name>` | Print one setting |
| `cctrace config set <key> <value>` | `--profile <name>` | Change one setting and rewrite the Claude settings file. Keys are listed in [Connect](../client/setup.md#view-and-change-settings). |

## Profiles

| Command | Flags | Does |
|---|---|---|
| `cctrace profile add <name>` | `--home <dir>` (required) | Copy the default profile into a named profile for another Claude home and wire that home |
| `cctrace profile list` | | List named profiles and their Claude homes |
| `cctrace profile remove <name>` | `--force` | Delete a named profile's directory. Leaves its Claude settings entries; use `cctrace reset --profile` to remove those too. |

See [Profiles](../client/profiles.md).

## Sync

| Command | Does |
|---|---|
| `cctrace sync` | One pass for the default profile and every named profile |
| `cctrace sync --daemon` | Start the background daemon for one profile |
| `cctrace sync --stop` | Stop the daemon gracefully |
| `cctrace kill` | Force-terminate the daemon holding the profile's sync lock. Takes `--profile`. |

| Flag | Default | Meaning |
|---|---|---|
| `--watch` | off | Run in the foreground and keep polling |
| `--interval <duration>` | `30s` | Poll interval for watch mode |
| `--daemon` | off | Run in the background |
| `--once` | off | With `--daemon`: one pass, then exit |
| `--stop` | off | Stop the running daemon |
| `--dry-run` | off | Show what would be sent without sending |
| `--claude-dir <dir>` | profile's Claude home | Claude config directory to read |
| `--profile <name>` | `CCTRACE_PROFILE` | Named profile |
| `--auto-profile` | off | Pick the profile whose Claude home matches `--claude-dir` or `CLAUDE_CONFIG_DIR` |
| `--profile-email <email>` | `CCTRACE_PROFILE_EMAIL` | Override the email attached to records |
| `--endpoint <url>` | profile's sync endpoint | Override the upload endpoint |
| `--local` | off | Development use: send to a server on localhost |

See [Sync daemon](../client/sync.md) for details.

## Removal

| Command | Flags | Does |
|---|---|---|
| `cctrace reset` | `--profile <name>`, `--all`, `--force` | Remove a profile and its Claude settings entries. Sync state is kept for the default profile. |
| `cctrace uninstall` | `--force` | Reset every profile, delete `~/.cctrace`, and delete the `cctrace` binaries on `PATH` |

See [Reset and uninstall](../client/uninstall.md).

## Environment variables read by the client

| Variable | Used by | Effect |
|---|---|---|
| `CCTRACE_PROFILE` | `sync`, `kill` | Named profile when `--profile` is not given |
| `CCTRACE_PROFILE_EMAIL` | `sync` | Email attached to records when `--profile-email` is not given |
| `CLAUDE_CONFIG_DIR` | `sync --auto-profile` | Claude home to match when `--claude-dir` is not given |
| `CODEX_CONFIG_DIR` | `init`, `sync` | Codex home in place of `~/.codex` |
| `CODEX_HOME` | `sync` | Additional Codex home to scan |
| `CCTRACE_CODEX_SYNC`, `CCTRACE_GJC_SYNC`, `CCTRACE_OMO_SYNC` | `sync` | `true` enables that agent's collection regardless of the profile option |

## Other commands

Read and analysis commands exist but are under active development and are not documented here: `report`, `usage`, `ls`, `events`, `projects`, `sessions`, `tools`, `plugins`, `skills`, `rules`, `organization-insights`, `insights`, `backfill-quota`, and `auth read`.
