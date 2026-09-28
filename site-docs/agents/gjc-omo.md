# GJC and OMO

cctrace collects GJC and OMO sessions by uploading their session files with the [sync daemon](../client/sync.md). Neither agent is configured for OTEL, and neither gets a session hook.

## Turn it on

Run the agent once so that its home directory exists (`~/.gjc` or `~/.omo`), then run `cctrace init`. For each directory it finds, `init` asks:

```text
  gjc detected (~/.gjc found).
  Enable gjc session sync? [Y/n]
```

Answering yes sets `options.gjc_sync_enabled` or `options.omo_sync_enabled` in the profile. Nothing is written into the agent's own configuration.

To turn either on after `init`:

```console
$ cctrace config set options.gjc_sync_enabled true
$ cctrace config set options.omo_sync_enabled true
```

Setting `CCTRACE_GJC_SYNC=true` or `CCTRACE_OMO_SYNC=true` in the environment of the sync process has the same effect, regardless of the profile option.

## Starting collection

Enabling the option does not start anything. The files are collected by whichever sync daemon runs for the profile:

- If you also use Claude Code with the same profile, the daemon started by its `SessionStart` hook collects GJC and OMO too.
- Otherwise start one yourself with `cctrace sync --daemon` or `cctrace sync --watch`, or run `cctrace sync` periodically.

The first pass skips content already on disk, so sessions from before you enabled the option are not uploaded.

## Files read

Homes scanned: `~/.gjc` plus each directory in `options.gjc_dirs` for GJC, and `~/.omo` plus each directory in `options.omo_dirs` for OMO. Under each home, these files are read:

```text
GJC   agent/sessions/v2-*/*.jsonl        session files
      agent/sessions/v2-*/*/*.jsonl      subagent transcripts
OMO   sessions/--*--/*.jsonl
      agent/sessions/--*--/*.jsonl
```

A configured directory that does not exist is skipped with a message in the log. `cctrace sync --dry-run` lists the homes a real sync would scan and how many session files each holds.

Sync progress is kept in `~/.cctrace/gjc-sync-state.json` and `~/.cctrace/omo-sync-state.json` (or the named profile's directory).
