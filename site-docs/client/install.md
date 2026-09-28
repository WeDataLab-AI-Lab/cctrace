# Install

Build the `cctrace` client from source and put it on your `PATH`. Every developer machine that should report sessions needs one.

## Requirements

- Go 1.25 or later (`go.mod`)
- `make`
- A running server. See [Server install](../server/install.md).

The client does not need Node.js or Docker. Those are only for the server and its dashboard.

## Build the client

`make build-client` builds one client for the current platform into `dist/`. Unlike `make build`, it does not need the dashboard build and runs on Linux as well as macOS.

```bash
make build-client
```

To build for another platform, use `make build-linux` (linux/amd64) or `make build-windows` (windows/amd64). Both also write into `dist/`.

!!! note "No automatic updates"
    A client built from this repository has an empty update public key, so it refuses automatic updates. To upgrade, rebuild and replace the binary yourself. See [Upgrading the client](sync.md#upgrading-the-client).

??? note "Default endpoints for a team build"
    `cctrace init` asks for the server's sync and OTEL endpoints. To pre-fill those prompts in the binaries you hand out, copy `deploy/local-defaults.env.example` to a file named local-defaults.env in the same directory, set `DEFAULT_SYNC_ENDPOINT` and `DEFAULT_OTEL_ENDPOINT`, then rebuild. Without that file the prompts start empty and each user types both addresses. That file is read only by `make`. For the binaries the server image serves (below), pass `--build-arg DEFAULT_SYNC_ENDPOINT=...` and `--build-arg DEFAULT_OTEL_ENDPOINT=...` to `docker build` instead.

## Download from your own server

The server image built in [Server install](../server/install.md) also cross-compiles the client and serves it from the server. If your machine can reach the server, you can download the client instead of building it:

| Platform | Path on the server |
|---|---|
| macOS, Apple silicon | `/downloads/cctrace-darwin-arm64` |
| macOS, Intel | `/downloads/cctrace-darwin-amd64` |
| Linux, x86-64 | `/downloads/cctrace-linux-amd64` |
| Linux, ARM64 | `/downloads/cctrace-linux-arm64` |
| Windows, x86-64 | `/downloads/cctrace-windows-amd64.exe` |

The paths are served at the address you use as the sync endpoint in `cctrace init`, including its port. Replace `<sync endpoint>` below with that address:

```console
$ curl -fL -o cctrace "<sync endpoint>/downloads/cctrace-darwin-arm64"
$ chmod +x cctrace
```

Use HTTPS or a network you trust for this download.

These binaries are built with version `dev` unless the server image was built with `--build-arg VERSION=...`. `cctrace --version` then prints `dev`, and `cctrace status` marks the binary as a development build. Like any client built from this repository, they do not update themselves.

## Put it on PATH

Put the binary where it will stay before you run `cctrace init`. The Claude Code hooks that `init` writes call the binary by the absolute path it was run from, so a hook written while running the binary in `dist/` keeps pointing into your checkout. If you move the binary later, run `cctrace env apply` from the new location to rewrite the hooks.

```console
$ sudo cp dist/cctrace /usr/local/bin/cctrace
$ cctrace --version
```

If you skip this, `init` offers to do it. When `cctrace init` finishes and `cctrace` is not on `PATH`, it asks:

```text
  [!] 'cctrace' is not in $PATH. Install to /usr/local/bin? (y/n) [y]:
```

- **macOS and Linux:** it copies the running binary to `/usr/local/bin/cctrace`. If the copy fails for lack of permission, it retries with `sudo`.
- **Windows:** it copies the binary into `AppData\Local\Programs\cctrace` under your home directory and adds that directory to your user `Path`. Open a new terminal afterwards.

If you answer no, or the copy fails, `init` prints manual options instead: `go install ./cmd/cctrace` (then add `~/go/bin` to `PATH`), or copying the binary into a directory that is already on `PATH`.

The copy happens after `init` has written the hooks, so they still name the binary `init` was run from. Run `cctrace env apply` once from the installed copy afterwards.

## Next step

[Connect the client to your server](setup.md).
