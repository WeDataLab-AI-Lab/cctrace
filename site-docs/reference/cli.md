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
| `cctrace config policy status` | `--profile <name>` | Print the installation ID, the profile name, and the profile's server policy state as JSON. Contacts no server. |
| `cctrace config policy check` | `--profile <name>` | Fetch the server's signed policy, verify it, probe its new addresses, and print the policy with the files an apply would change (when no policy applies, or it is already applied, it prints the same state as `status`). Writes no settings and no policy state. Needs an update public key or a policy public key. |
| `cctrace config policy apply` | `--profile <name>` | Fetch, verify, and apply the server's signed policy, then report the result to the server if the profile is logged in. Needs an update public key or a policy public key. |

`config policy` works with the signed server policy described in [Sync daemon](../client/sync.md#server-policy). `check` and `apply` verify it with the update public key or the policy public key linked into the client. A client built from this repository without an update or policy public key has neither, so both end with `policy public key is not configured; rebuild with main.policyPublicKeyBase64 or main.updatePublicKeyBase64`, while `status` still works. `apply` reports that failure to the server too when the profile is logged in. The installation ID is a random UUID in `~/.cctrace/installation-id`; whichever of `status`, `check`, and `apply` runs first creates it. What a report contains is listed in [Privacy](privacy.md).

## Profiles

| Command | Flags | Does |
|---|---|---|
| `cctrace profile add <name>` | `--home <dir>` (required) | Copy the default profile into a named profile for another Claude home and wire that home |
| `cctrace profile list` | | List named profiles and their Claude homes |
| `cctrace profile edit <name>` | `--home <dir>`, `--codex-home <dir>` | Change the Claude home or Codex home of a named profile without rewriting any settings file. Run `cctrace env apply --profile <name>` afterwards to write the OTEL variables and hooks into a new Claude home. |
| `cctrace profile remove <name>` | `--force` | Delete a named profile's directory. Leaves its Claude settings entries; use `cctrace reset --profile` to remove those too. |
| `cctrace profile claim <name>` | `--source <id\|folder>` (repeatable), `--all`, `--force` | Give the profile a source it shares with another profile, which the other profile lets go of first. Needs the local daemon running; asks for confirmation unless `--force`. |

See [Profiles](../client/profiles.md).

## Sync

| Command | Does |
|---|---|
| `cctrace sync` | One pass for the default profile and every named profile |
| `cctrace sync --daemon` | Start the background daemon for one profile |
| `cctrace sync --stop` | Stop the daemon gracefully |
| `cctrace sync status` | Print what the running daemon last published as a table: profiles and the sources held back from them. `--json` prints the raw snapshot. |
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

## Local analysis

Commands that serve this machine's session logs to the dashboard in your browser. Serving uploads nothing; with `local enable`, the sync daemon still uploads as the profile's options say. See [Local analysis](../client/local.md).

| Command | Flags | Does |
|---|---|---|
| `cctrace local serve` | `--origin` (required), `--address`, `--claude-dir`, `--codex-dir` | Serve the local session API on a loopback address in the foreground until you stop it. Prints the endpoint and a new random token as JSON. The token is not stored. |
| `cctrace local enable` | `--origin` (required), `--profile <name>` | Allow the profile's sync daemon to serve the local session API for that origin on a fixed loopback port. Prints the endpoint and a token once; only a hash of the token is kept. Running it again issues a new token and picks a new port. |
| `cctrace local status` | `--profile <name>` | Print as JSON whether local analysis is enabled and running, with its origin and address |
| `cctrace local disable` | `--profile <name>` | Revoke the token and turn local analysis off for the profile. A daemon that is serving stops serving. |

| Flag of `local serve` | Default | Meaning |
|---|---|---|
| `--origin <origin>` | required | Web origin allowed to call the API. Repeatable. |
| `--address <ip:port>` | `127.0.0.1:0` | Loopback IP address and port to listen on. Port 0 picks a free port. |
| `--claude-dir <dir>` | see below | Claude config directory to read. Repeatable. |
| `--codex-dir <dir>` | see below | Codex home to read. Repeatable. |

With neither `--claude-dir` nor `--codex-dir`, `local serve` reads the default Claude home, the default Codex home and `CODEX_HOME`, and the Claude and Codex homes configured for each profile. With either flag, it reads only the directories given.

## Removal

| Command | Flags | Does |
|---|---|---|
| `cctrace reset` | `--profile <name>`, `--all`, `--force` | Remove a profile and its Claude settings entries. Sync state is kept for the default profile. |
| `cctrace uninstall` | `--force` | Reset every profile, delete `~/.cctrace`, and delete the `cctrace` binaries on `PATH` |

See [Reset and uninstall](../client/uninstall.md).

## Environment variables read by the client

| Variable | Used by | Effect |
|---|---|---|
| `CCTRACE_PROFILE` | `sync`, `kill`, `config policy` | Named profile when `--profile` is not given |
| `CCTRACE_PROFILE_EMAIL` | `sync` | Email attached to records when `--profile-email` is not given |
| `CLAUDE_CONFIG_DIR` | `sync --auto-profile` | Claude home to match when `--claude-dir` is not given |
| `CODEX_CONFIG_DIR` | `init`, `sync` | Codex home in place of `~/.codex` |
| `CODEX_HOME` | `sync` | Additional Codex home to scan |
| `CCTRACE_CODEX_SYNC`, `CCTRACE_GJC_SYNC`, `CCTRACE_OMO_SYNC` | `sync` | `true` enables that agent's collection regardless of the profile option |

## Other commands

Read and analysis commands exist but are under active development and are not documented here: `report`, `usage`, `ls`, `events`, `projects`, `sessions`, `tools`, `plugins`, `skills`, `rules`, `organization-insights`, `insights`, `backfill-quota`, and `auth read`.
