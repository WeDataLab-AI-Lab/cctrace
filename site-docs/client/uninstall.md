# Reset and uninstall

`cctrace reset` removes a profile and its Claude Code wiring. `cctrace uninstall` removes every profile, the `~/.cctrace` directory, and the binary.

Neither command deletes data already uploaded to the server.

## Before you start

Stop the sync daemon for each profile you are removing. Neither command stops a running daemon.

```console
$ cctrace sync --stop
$ cctrace sync --stop --profile work
```

## Reset

```bash
cctrace reset
```

Asks `Remove default cctrace configuration? [y/N]`, then:

1. Removes cctrace's entries from the default profile's Claude settings file (`~/.claude/settings.json` unless the profile names another Claude home):
    - the OTEL variables that cctrace manages under `env`, even if you had set any of them yourself;
    - the cctrace `SessionStart` and `SessionEnd` hooks, and any hook group or event left empty by that;
    - the `cctrace` stamp object.
2. Deletes `~/.cctrace/profile.json`.

Everything else under `~/.cctrace` stays: the sync state files, the logs, and any named profiles. `reset` lists the named profiles that remain. Because the sync state is kept, running `cctrace init` again continues from where collection stopped.

| Flag | Effect |
|---|---|
| `--profile <name>` | Reset only this named profile: remove its entries from its Claude home's settings file and delete `~/.cctrace/profiles/<name>/`, including that profile's sync state and log |
| `--all` | Reset the default profile and then every named profile |
| `--force` | Skip the confirmation prompt |

`reset` does not change `~/.codex/config.toml`. To stop Codex from sending metrics, delete the `[otel]` section from that file yourself. See [Codex CLI](../agents/codex.md).

## Uninstall

```bash
cctrace uninstall
```

Lists what it will do and asks `Proceed? [y/N]`. Only `y` continues. `--force` skips the prompt. Then it:

1. Runs the equivalent of `cctrace reset --all --force`: cctrace's entries are removed from the settings file of every profile's Claude home, and every profile is deleted.
2. Deletes the whole `~/.cctrace` directory, including sync state and logs.
3. Deletes every `cctrace` binary it finds in the directories on your `PATH`, in `/usr/local/bin`, and in `~/go/bin`. Symbolic links are resolved to the file they point to. On macOS and Linux, a file it cannot delete is retried with `sudo rm`. On Windows, a binary that is in use is renamed and deleted a few seconds later.

A binary it cannot remove is reported with `remove manually`.

!!! note
    `uninstall` deletes the sync state together with `~/.cctrace`. If you install again later, the first sync treats the existing content of every session file as history and skips it.

Like `reset`, `uninstall` leaves the `[otel]` section in `~/.codex/config.toml` in place.
