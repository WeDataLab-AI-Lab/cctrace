# Profiles

Use one profile per Claude config directory when you run Claude Code with more than one account on the same machine.

## How profiles map to accounts

Claude Code keeps each login in its own config directory: `~/.claude` by default, or another directory selected with `CLAUDE_CONFIG_DIR`. cctrace follows the same split.

| Profile | Stored in | Claude home |
|---|---|---|
| Default | `~/.cctrace/profile.json` | `~/.claude` |
| Named, for example `work` | `~/.cctrace/profiles/work/profile.json` | The directory the profile names, for example `~/.claude-work` |

Each profile carries its own server endpoints, cctrace user identity, upload token, and options, and writes its OTEL variables and hooks into the settings file of its own Claude home. The sync daemon, its lock, its log, and its sync state are also per profile. See [Sync daemon](sync.md).

Which cctrace user a profile reports as depends on how it was created:

- `cctrace init --profile <name>` authenticates on its own, so the profile can belong to a different cctrace user than the default one.
- `cctrace profile add` and the directories `init` detects copy the default profile's user, server, and options. They report as the same cctrace user.

Profile names must start with a letter or digit and may contain letters, digits, `-` and `_` (up to 63 characters). `default` is reserved.

## Automatic detection during init

At the end of `cctrace init` for the default profile, `init` looks for directories in your home directory whose name starts with `.claude-` and that contain a projects directory or a settings file (settings.json). `.claude-mem` is ignored, as is any directory another profile already uses.

```text
  Detected 2 additional Claude home(s):
    1. /Users/alice/.claude-work
    2. /Users/alice/.claude-personal

  Set up sync for which? (all/none/1,2,...) [all]:
```

For each directory you pick, `init` creates a named profile whose name is the directory name without the leading dot (`claude-work`), copies the default profile into it, and writes that directory's settings file.

## Add a profile

```console
$ cctrace profile add claude-4 --home ~/.claude-4
```

`profile add` copies the default profile, points it at the directory given by `--home`, and writes the OTEL variables and hooks into that directory's settings file. `--home` is required. The default profile must exist first.

It refuses a name that already exists and a Claude home that another named profile already uses. If the directory does not exist yet, it warns and creates the profile anyway.

To set up a profile with its own login instead, run init with a name:

```console
$ cctrace init --profile work
```

This runs the full setup and adds one prompt, `Claude config directory (Enter for default ~/.claude)`. `init` warns if another profile already uses the directory you enter.

## List profiles

```bash
cctrace profile list
```

Prints each named profile with its Claude home. The default profile is not listed; `cctrace status` shows both.

## Remove a profile

```bash
cctrace profile remove work
```

Deletes the directory under `~/.cctrace/profiles/` for that profile after asking for confirmation (`--force` skips it). It does not touch the profile's Claude settings file, so the OTEL variables and hooks stay there. To remove those as well, use `cctrace reset --profile work` instead. See [Reset and uninstall](uninstall.md).

## Select a profile on the command line

Commands that act on one profile take `--profile <name>`: `status`, `config`, `env apply`, `sync`, `kill`, and `reset`. `sync` and `kill` also read the `CCTRACE_PROFILE` environment variable when the flag is not given.

The hooks written into a Claude home pass `--claude-dir` with that home and `--auto-profile`. `--auto-profile` picks the named profile whose Claude home matches the `--claude-dir` value (or, without that flag, `CLAUDE_CONFIG_DIR`). When no named profile matches, the default profile is used.
