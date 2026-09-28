# Claude Code

cctrace collects Claude Code through two paths: OTEL telemetry that Claude Code sends to the server itself, and session logs that the [sync daemon](../client/sync.md) uploads. Both are configured in Claude Code's settings file.

## Where the settings go

`cctrace init`, `cctrace env apply`, `cctrace profile add`, and `cctrace config set` write into the settings file of the profile's Claude home: `~/.claude/settings.json` for the default profile, or the directory a named profile points at. See [Profiles](../client/profiles.md).

The write keeps every key cctrace does not manage. Before writing, cctrace takes a snapshot and refuses to write if anything other than its own keys would change. It also refuses to overwrite a settings file that is not valid JSON. The file is replaced atomically and written with mode 0600.

## OTEL environment

These variables are set under `env`:

| Variable | Value |
|---|---|
| `CLAUDE_CODE_ENABLE_TELEMETRY` | `1` |
| `OTEL_METRICS_EXPORTER` | `otlp` |
| `OTEL_LOGS_EXPORTER` | `otlp` |
| `OTEL_METRICS_INCLUDE_ACCOUNT_UUID` | `1` |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | the profile's protocol, `grpc` by default |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | the profile's OTEL endpoint |
| `OTEL_EXPORTER_OTLP_HEADERS` | `Authorization=Bearer <upload token>` |
| `OTEL_METRIC_EXPORT_INTERVAL` | `60000` by default (`options.metrics_export_interval`) |
| `OTEL_LOGS_EXPORT_INTERVAL` | `5000` by default (`options.logs_export_interval`) |
| `OTEL_BSP_MAX_QUEUE_SIZE` | `4096` |
| `OTEL_BSP_SCHEDULE_DELAY` | `5000` |
| `OTEL_BSP_MAX_EXPORT_BATCH_SIZE` | `512` |
| `OTEL_BSP_EXPORT_TIMEOUT` | `30000` |
| `OTEL_RESOURCE_ATTRIBUTES` | the profile's user ID, name, email, and team, URL-escaped (example below) |

```text
OTEL_RESOURCE_ATTRIBUTES=user.id=alice,user.name=Alice%20Doe,user.profile.email=alice@example.com,user.team=platform
```

An attribute is left out when the profile field is empty.

cctrace owns these keys. If you had set any of them yourself, `init` overwrites it and `reset` removes it.

## Session hooks

When session log sync is enabled (`options.sync_enabled`), two hooks are added under `hooks`, each in the group with an empty `matcher`:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/cctrace sync --daemon --log-to-file --claude-dir /Users/alice/.claude --auto-profile --interval 1s"
          }
        ]
      }
    ],
    "SessionEnd": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/cctrace sync --daemon --once --log-to-file --claude-dir /Users/alice/.claude --auto-profile",
            "async": true,
            "timeout": 30
          }
        ]
      }
    ]
  }
}
```

- The command starts with the absolute path of the binary that wrote it. A binary in a temporary directory is not used; cctrace falls back to the `cctrace` found on `PATH`. After moving the binary, run `cctrace env apply`.
- `--claude-dir` is the profile's Claude home, and `--auto-profile` selects the matching profile. See [Sync daemon](../client/sync.md) for the flags.
- `SessionStart` starts the daemon for the profile. `SessionEnd` runs one more pass, asynchronously with a 30-second timeout, and leaves the daemon running.
- Existing hooks are kept. If a cctrace hook is already present, it is updated in place instead of added again.

Turning `options.sync_enabled` off later does not remove the hooks; the hook then runs and exits without syncing. `cctrace reset` removes them.

## Self-heal stamp

cctrace also writes a top-level `cctrace` object holding a hash of what it generated. Each time the sync daemon starts, it recomputes the hash; if an upgraded binary would write different variables or hooks, it rewrites the settings file. `reset` removes the object.

## When changes take effect

Claude Code reads the environment and hooks when a session starts. After `init`, `env apply`, or `config set`, start a new Claude Code session. A session that was already running sends no telemetry, and its session log is uploaded only from the point the daemon first sees it.
