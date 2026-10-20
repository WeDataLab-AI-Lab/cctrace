# Local analysis

Local analysis shows the Claude Code and Codex CLI sessions stored on this machine in the dashboard, without needing a server. cctrace opens a small API on a loopback address that reads the session files, and a dashboard page in your browser connects to it with an address and a token.

The API itself sends nothing to a server, and it needs neither a server login nor a database. It does not replace the [sync daemon](sync.md).

## Two ways to run it

| | `cctrace local serve` | `cctrace local enable` |
|---|---|---|
| Runs as | A foreground command, until you press Ctrl-C | Part of the profile's sync daemon |
| Port | Chosen by the operating system at each start, unless `--address` sets one | Chosen when you run `enable`; the same on every daemon start |
| Token | New on every start, printed once | New on every `enable`, printed once; only its hash is stored |
| Pages allowed | One or more `--origin` values | The one `--origin` value |
| Folders read | The defaults below, or the ones you name | The profile's Claude home and the Codex homes its daemon scans |
| Needs a profile | No | Yes |

## Run it once

```bash
cctrace local serve --origin http://127.0.0.1:8080
```

`--origin` is required. It is the exact origin of the dashboard page that will connect, written as the browser shows it: scheme, host, and the port when the page uses a non-default one, with no path and no trailing slash. For a dashboard behind a proxy, such as `https://cctrace.company.example`, give that address instead. Repeat `--origin` to allow more than one page. A wildcard, a path, a query string, or credentials in the origin are refused.

The first line of output is a JSON object. A second line says the API is active and that the token must stay private.

- `endpoint`: the loopback address and port the API listens on
- `token`: the bearer token for this run
- `profile_fingerprint`: an identifier for the set of folders being read
- `origins`: the allowed origins

The token is 32 random bytes, new on every start. Do not redirect the output to a file, put the token in a URL, or paste it into a report. Press Ctrl-C to stop the API; the token stops working with it.

| Flag | Default | Effect |
|---|---|---|
| `--origin <origin>` | none, required | Origin of a dashboard page allowed to call the API. Repeatable. |
| `--address <ip:port>` | `127.0.0.1:0` | Address to listen on. It must be a literal loopback IP address; a name such as `localhost` is refused. Port 0 lets the operating system choose. |
| `--claude-dir <dir>` | see below | Claude config directory to read. Repeatable. |
| `--codex-dir <dir>` | see below | Codex home to read. Repeatable. |

### Folders it reads

Without `--claude-dir` or `--codex-dir`, `serve` reads these folders:

- Claude Code: the folder `CLAUDE_CONFIG_DIR` names, or `~/.claude`, and the Claude home of each profile that sets one.
- Codex CLI: `CODEX_CONFIG_DIR`, or `~/.codex`; `CODEX_HOME` when it is set; and the Codex home of each profile that sets one. The profile option `options.codex_dirs` is not consulted.

A folder that does not exist is skipped. Passing either flag switches off all of the above: `serve` then reads only the folders you name and adds no profile's folders. With `--claude-dir` alone no Codex session is read, and the reverse.

Inside a Claude folder the API reads JSONL session files under `projects`; inside a Codex folder, the rollout files under `sessions` (the layout in [Codex CLI](../agents/codex.md)). A file that resolves to a place outside its folder is ignored, and a symbolic link in place of the `projects` or `sessions` directory is not followed.

## Run it with the daemon

```bash
cctrace local enable --origin https://cctrace.company.example
```

Add `--profile <name>` to act on a named profile. `enable` prints a JSON object with `endpoint` and `token` once. It stores the origin, a free port on 127.0.0.1, and the SHA-256 hash of the token (not the token) in the profile directory: `~/.cctrace/local-api-policy.json` for the default profile, `~/.cctrace/profiles/<name>/local-api-policy.json` for a named one. It also turns on the profile option `options.local_analysis_enabled`.

The API starts together with the profile's sync daemon, so restart a daemon that was already running (`cctrace sync --stop`, then `cctrace sync --daemon`). It reads that profile's Claude home and the Codex homes the daemon scans, listed in [Codex CLI](../agents/codex.md). If the stored port is taken when the daemon starts, the API does not start and `status` reports the error. `enable` does not change the profile's sync settings: the daemon keeps uploading as the profile's options say.

```bash
cctrace local status
cctrace local disable
```

`status` prints one JSON object: `enabled`, `running`, `origin`, `address`, `generation`, `started_at`, `ended_at`, and `error`. `disable` clears the stored token hash and turns the option off, and a running daemon stops the API when it notices.

Running `enable` again issues a new token and a new port. The previous token stops working and the running daemon stops its API, so restart the daemon to start it with the new values.

## Connect from the dashboard

Two pages use the connection:

- `/local-sessions` works without signing in to the dashboard.
- The **내 기기와 서버** view on the Sessions page needs a signed-in dashboard session, because it lists server sessions next to the local ones.

Open the page at the origin you gave to `--origin`. Enter the `endpoint` value in **로컬 서비스 주소** and the token in **접속 토큰**, then press **연결**. The form accepts only `http://` followed by `localhost`, 127.0.0.1, or `[::1]` and a port, and it rewrites `localhost` to 127.0.0.1 before it connects. The API accepts only the exact address it listens on. Type the `endpoint` value as printed: an address with 127.0.0.1 in `endpoint` is refused if you write `[::1]` instead.

**이 탭에 연결 저장** keeps the address and token in the `sessionStorage` of that browser tab, so a reload reconnects without pasting them again. Closing the tab forgets them. Without the box checked, the token stays in the page's memory and a reload asks for it again. **연결 끊기** clears the saved pair.

The session list has a search box and an agent filter (**전체**, **Claude**, **Codex**). Token counts, event counts and cost in the list show **미집계** (not tallied) instead of zero, because the list reads only the first and last 32 KiB of each file. Selecting a session opens three tabs: **대화**, **개별 원문**, and **분석**. The analysis tab reads the whole file and computes what the records support; a value they cannot support stays empty.

In the **내 기기와 서버** view, a local session and a server session with the same session ID are listed apart. The page offers **같은 세션으로 묶기** to group them by hand, because the same ID alone does not show that both belong to the same person.

## What the API allows

- It listens on a loopback address only. `serve` refuses any other `--address`, and `enable` always uses 127.0.0.1.
- Every request except a browser's CORS preflight needs the token. The `Host` header must equal the listen address, which refuses a request that reaches the API under another name, such as a DNS-rebinding page.
- A request that carries an `Origin` header outside the allowed list is refused.
- It reads session files and does not modify them. The one write is the choice of profile for the next Claude Code or Codex run, which a page can save through `serve`, stored in `~/.cctrace/next-profile.json`; the daemon's API has no profile choice.
- It does not check whether a local session also exists on the server, or who owns it.
- Responses are marked `no-store`.

An HTTPS dashboard page may need the browser's local network permission before it can reach a loopback address. Allowing an origin in the CLI does not grant that permission, and does not show that every browser supports the connection.
