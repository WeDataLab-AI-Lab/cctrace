# Codex CLI

cctrace collects Codex CLI through OTEL metrics that Codex sends to the server, and through the session files that the [sync daemon](../client/sync.md) uploads.

## Turn it on

Run Codex once so that `~/.codex` exists, then run `cctrace init`. When `init` finds that directory it asks:

```text
  Codex CLI detected (~/.codex found).
  Enable Codex session sync? [Y/n]
```

Answering yes sets `options.codex_sync_enabled` in the profile and writes an `[otel]` section into `~/.codex/config.toml`.

If you installed Codex after `init`, run `cctrace init` again and choose `Patch Codex integration only`. It keeps the existing endpoints and token, turns the option on, and writes the `[otel]` section without asking for your password.

`init` looks for the Codex home at `CODEX_CONFIG_DIR` when that variable is set, and at `~/.codex` otherwise.

!!! note "Codex is turned on automatically, unless you turned it off"
    Every sync run checks for the Codex home. If it exists, the profile has an OTEL endpoint and an upload token, and `options.codex_sync_enabled` is off, the sync turns the option on and writes the `[otel]` section. This covers profiles created before Codex support.

    Answering no during `init`, or running `cctrace config set options.codex_sync_enabled false`, is recorded in the profile as `options.codex_sync_declined`, and the sync then leaves every Codex home alone. Running `cctrace config set options.codex_sync_enabled true`, accepting the prompt in `init`, or choosing `Patch Codex integration only` clears it.

## OTEL configuration

The section cctrace writes looks like this:

```toml
[otel]
metrics_exporter = { otlp-http = { endpoint = "http://cctrace.company.example:4318/v1/metrics", protocol = "binary", headers = { Authorization = "Bearer <upload token>", X-Cctrace-Codex-Account = "<account id>" } } }
```

- The endpoint is derived from the profile's OTEL endpoint: port 4317 becomes 4318, and the path becomes `/v1/metrics`. Codex sends over OTLP/HTTP, not gRPC.
- Only a metrics exporter is configured. cctrace sets up no OTEL log export for Codex.
- `X-Cctrace-Codex-Account` carries the Codex billing account id from `<Codex home>/auth.json`, so the server can apply billing-account exclusions to Codex metrics. It is left out when the account is unknown and rewritten by the sync when the account changes. Codex reads the new section when it restarts.
- Codex sends no metrics to an `https://` endpoint and reports no error. `init`, the Codex patch and the sync print a `[!]` warning whenever they write such an endpoint. Use plain `http://` on port 4318.
- cctrace manages the whole section and may add further headers to it. Any existing `[otel]` section, in inline or table form, is replaced. The rest of the file is kept. The file is replaced atomically and written with mode 0600.
- Codex metrics do not reach the server through the TLS overlay described in [Server install](../server/install.md). cctrace derives Codex's endpoint by mapping port 4317 to 4318 only, so an OTEL endpoint on 5317 sends Codex's OTLP/HTTP to the gRPC listener. Editing the section by hand does not help: the next sync rewrites it.
- On each sync run the section is compared with what the current profile would produce and rewritten if it differs, for example after you change the OTEL endpoint with `cctrace config set`.

## Session files

The sync daemon uploads Codex session files that match either of these under each Codex home:

```text
sessions/rollout-*.jsonl
sessions/YYYY/MM/DD/rollout-*.jsonl
```

Codex homes scanned, in order, with duplicates removed:

1. `CODEX_CONFIG_DIR`, or `~/.codex`
2. `CODEX_HOME`, when set (in addition to the default, not instead of it)
3. Each directory in `options.codex_dirs`

The sync also rewrites the `[otel]` section of the extra homes (2 and 3) to the current format and that home's account header, but only while Codex sync is on and only when the home's `[otel]` endpoint already points at this cctrace server (same host and port). A home without an `[otel]` section is not touched, one that points at another server is named in the log and left alone, and a `<Codex home>/config.toml` that is a symbolic link is skipped with a message. The sync keeps the Bearer token already in an extra home and prints a line suggesting `cctrace init` when it differs from the profile's. `cctrace init` and the Codex patch replace it with the profile's token.

A directory in `options.codex_dirs` that does not exist is skipped with a message in the log. `cctrace sync --dry-run` lists the homes a real sync would scan and how many session files each holds.

Setting `CCTRACE_CODEX_SYNC=true` in the environment of the sync process also enables Codex collection, regardless of the profile option.

## Starting collection

Codex has no session hook. Its files are collected by whichever sync daemon is running for the profile:

- If you also use Claude Code with the same profile, the daemon that Claude Code's `SessionStart` hook starts collects Codex too.
- If you use only Codex, start the daemon yourself with `cctrace sync --daemon`, or run `cctrace sync` periodically.

As with every agent, the first pass skips content that is already on disk, so `Synced 0 records` right after enabling Codex is expected. Collection starts with content written after that.

## Removing the configuration

`cctrace reset` and `cctrace uninstall` do not change `~/.codex/config.toml`. Delete the `[otel]` section yourself to stop Codex from sending metrics.
