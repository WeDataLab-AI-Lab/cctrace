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

## Sources shared between profiles { #sources-shared-between-profiles }

When two profiles that upload to the same destination share a source folder, the daemon lets one profile, the holder, collect the overlapping source and holds the other back from it. The rest of each profile keeps collecting. Which profile holds a source, how to move it, and what nested folders mean are in [Profiles](profiles.md#source-folders-that-profiles-share). A source is held back from the moment the daemon decides. A profile that starts with no saved progress at all skips the sessions of that period, as with the first-pass skip above. A profile that already has saved progress goes on from it, so it can send files of that period too; the server drops what it already has.

A one-shot `cctrace sync`, the `SessionEnd` pass, and re-enrichment follow the same decision. They read a source only when the running daemon would let that profile read it, and report one they leave out on stderr, or in the log for a background pass:

```text
profile=work [routes] kind=codex source=3fa91c2d was not read: another profile holds it
```

A folder is judged by which folder it is, not by how it is spelled or where it came from. A folder given by a flag or an environment variable is left out too when it overlaps a source that the daemon's decision gives to another profile that uploads to the same destination. The line reads `source=-` when no source id is known, for example while the routes file cannot be used. These runs also leave out a source that was handed to the profile a moment ago (`it was just handed to this profile, and the daemon reads it first`): the daemon's job reads it first, from the previous holder's progress.

### `cctrace sync status`

`cctrace sync status` prints what the running daemon last published as a table; `--json` prints the raw snapshot instead. A section appears only when it has rows.

```text
daemon   pid 4121   version 1.2.3   started 2026-10-15T09:12:03Z   observed 3s ago   complete yes

PROFILE   STATE    SENT  LAST PASS             NEXT RETRY  CODE
default   healthy  1204  2026-10-15T09:31:10Z  -           -
work      healthy   311  2026-10-15T09:31:08Z  -           -
personal  blocked     0  -                     -           duplicate_route

SHARED SOURCES (collected by another profile, not by the one on the left)
PROFILE   KIND    SOURCE    LABEL    REASON  HELD BY  HOLDER STATE
work      codex   3fa91c2d  default  same    default  active
personal  claude  77aa10c2  home     same    default  active
```

| Column | Meaning |
|---|---|
| `CODE` | `duplicate_route` when every source of the profile is shared, so the profile collects nothing |
| `SOURCE` | The source's id, as `cctrace profile claim --source` takes it |
| `LABEL` | Where the folder came from: `home`, `default`, `env`, `configured`, or `explicit` |
| `REASON` | `same` for one folder, `nested` for folders inside each other, where some files may be collected by no profile |
| `HELD BY`, `HOLDER STATE` | The holder, and `active`, `stopped`, `disabled`, `blocked`, `transferring`, `removed`, `unowned`, or `unknown` |

A third section, `DOWNGRADE`, lists sources that an older build of the daemon could not represent, each with the command that clears it: `cctrace profile claim <profile> --source <id>`. It matters only if you go back to such a build.

### Daemon log

The daemon writes one `[routes]` line when a decision changes, not on every pass. A line carries names, counts, and ids, never a folder, an address, or a token. A state line, such as `excluded`, is written again only if the state ends and comes back.

| Line | Meaning |
|---|---|
| `[routes] reserved profile=default kind=codex source=3fa91c2d label=default destination=1a2b3c4d` | The profile now holds the source. `destination` is the first eight characters of a hash. |
| `[routes] excluded profile=work kind=codex source=3fa91c2d label=default reason=same holder=default holder_state=active` | The profile is held back from the source |
| `[routes] fully-shared profile=work sources=1 holders=default` | Every source of the profile is held back |
| `[routes] releasing source=3fa91c2d holder=default to=work cause=claim` | The holder is letting go of the source, to the named profile (`to=none` if nobody takes it), because of `claim`, `source_removed`, or `collision` |
| `[routes] releasing source=3fa91c2d holder=work to=none cause=collision` | Two sources that were held separately turned out to be one, for example after a folder was replaced by a link to another folder. The hold made later is let go of, and no profile reads the source until its holder has stopped reading it. |
| `[routes] released source=3fa91c2d holder=default cause=source_removed successor=none` | The holder let go and no profile took the source |
| `[routes] removed source=3fa91c2d holder=work cause=collision` | The holder of the later hold has stopped reading, and the hold was removed. The earlier hold's holder reads the source from now on. |
| `[routes] handoff source=3fa91c2d from=default to=work` | The source changed hands |
| `[routes] orphan source=3fa91c2d former_holder=default` | The holder was removed; nobody reads the source until a claim |
| `[routes] invalid reason=unreadable` | The routes file could not be used. Overlapping sources stay unread until `cctrace profile claim` rebuilds it. |
| `[routes] write-failed rev=8 reason=io` | The routes file could not be written, so the new decision is not applied and the write is tried again. The line is written once while the failure lasts. `reason` is `io`, or the word of the check that refused the file. |

## Server policy

A server operator can publish one signed policy that moves a client to new server addresses and a new certificate authority. The client verifies it with the update public key or the policy public key linked into the binary, so only a client built with one of them applies a policy. A client built from this repository without an update or policy public key has neither (see [Upgrading the client](#upgrading-the-client)): it never starts a policy check by itself, and `cctrace config policy apply` run by hand fails with `policy public key is not configured; rebuild with main.policyPublicKeyBase64 or main.updatePublicKeyBase64`. A client built with your own public key, passed as the `UPDATE_PUBLIC_KEY` or `POLICY_PUBLIC_KEY` Make variable or Docker build argument, applies a policy signed with the matching private key.

A client with the key starts a background policy check, `cctrace config policy apply`, from `cctrace sync` at three points:

- when a watch process starts
- about every five minutes while it runs
- at the end of a one-shot `cctrace sync`

No check starts for a run with `--dry-run`, `--local`, or `--endpoint`.

The check asks the server for a policy by sending the installation ID and the profile name; this request carries no upload token. The installation ID is a random UUID that `cctrace` creates in `~/.cctrace/installation-id` on first use and that all profiles on the machine share. When the server has no policy for this client, nothing changes. Otherwise the client tests the policy's new addresses before applying it, and the test requests to the OTLP addresses carry the profile's upload token.

When a policy applies, the client writes the policy's certificate authority into the profile directory and rewrites the profile's server addresses, the Claude settings file in its Claude home, and, unless Codex sync is turned off for the profile, the Codex configuration file in the default Codex home, in the directory `CODEX_HOME` names when it is set, and in each directory of `options.codex_dirs`. A profile's explicit `codex_home` is handled on its own when Codex sync is on for the profile: the client moves the `[otel]` section there only if it is the section cctrace generates for the profile, keeping the token it carries, and otherwise leaves the file as it is without deferring the policy; see [Server policy deferral](profiles.md#server-policy-deferral). If the sync daemon is running, the client stops it and starts it again with the settings it ran with. A daemon started with `--local` or `--endpoint` cannot be moved this way: `apply` then changes nothing and ends with an error. Running Claude Code and Codex sessions use the new settings the next time they start.

A policy that would change a file another profile also uses is deferred instead of applied, unless it is a Codex configuration file and every profile that uses it moves to the same server; see [Server policy deferral](profiles.md#server-policy-deferral). After every run of `cctrace config policy apply`, the client reports the result to the server when the profile is logged in; see [Privacy](../reference/privacy.md).

## Logs and state files

Each profile keeps these files in its profile directory: `~/.cctrace/` for the default profile, `~/.cctrace/profiles/<name>/` for a named one.

| File | Contents |
|---|---|
| `~/.cctrace/sync.log` | Daemon log. Rotates at 10 MB and keeps 5 old files. |
| `~/.cctrace/sync-crash.log` | Raw output of the background process, such as panics. Trimmed when it passes 1 MB. |
| `~/.cctrace/sync-state.json` | How far each Claude Code session file has been read |
| `~/.cctrace/codex-sync-state.json`, `~/.cctrace/gjc-sync-state.json`, `~/.cctrace/omo-sync-state.json` | The same for Codex, GJC, and OMO |
| `~/.cctrace/server-policy-state.json` | Server policy state: the last applied policy number, the latest status and reason, the last 32 results, and shared Codex configuration writes still waiting (path, digests of the old and new `[otel]` sections, old OTEL address and certificate path; no token) |
| `~/.cctrace/sync.lock`, `~/.cctrace/sync.pid`, `~/.cctrace/sync-runtime.json` | Single-instance lock and the running daemon's pid and version |

Keep the state files. A missing sync state file counts as a first run, so the next pass skips the existing content of every file instead of uploading it. A missing server policy state file makes the client forget which policy it applied, and the same policy can then fail to apply again; shared Codex configuration writes still waiting are dropped with it.

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

Clients built from this repository without an update public key never replace themselves; for clients that do, see [Security operations](../server/security.md#when-a-client-updates-itself). A server built as described in [Server install](../server/install.md) reports version `dev`, and the client then skips the update check. If the server reports a newer release version, the attempt fails because the client has an empty update public key, and `~/.cctrace/sync.log` records the reason on an `update:` line, for example `update: update public key is not configured`. Collection continues on the current binary either way.

To upgrade:

1. Rebuild the client (see [Install](install.md)) and copy it over the installed binary.
2. If the new binary is at a different path, run `cctrace env apply` from it so that the hooks point at it.
3. Restart the daemon so that it runs the new code: `cctrace sync --stop`, then `cctrace sync --daemon` or start a new Claude Code session.

While an older daemon is still running, `cctrace status` shows a notice under `수집 상태` naming both versions.

## Membership, upload exclusion, and collection execution

The multi-home profile change is under implementation and validation. Membership groups configuration; it is separate from upload permission and whether collection is running. A home allowed to upload still sends nothing while its profile collection is stopped. Usage monitoring remains independent.

Excluded accounts remain in the same list, last, with a gray/pale-red whole-card treatment and an explicit label. Both directions of `Exclude session uploads` require confirmation before saving. Neither direction moves profiles, starts collection, or changes usage monitoring.

Local upload permission cannot override server-enforced exclusions. Direct provider OTEL and session-file uploads are different paths; excluding session uploads does not by itself disable all telemetry.
