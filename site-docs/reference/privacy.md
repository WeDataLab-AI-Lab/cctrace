# Privacy

What cctrace collects, where it keeps it, who can read it, and how long it stays.

!!! warning "The server stores conversation content"
    Session logs include what people typed, the agent's replies, and tool commands and output. By default they are kept until an operator sets a retention period. Decide on retention and redaction before you connect other people.

## What is collected

| Data | Sent by | Contents |
|---|---|---|
| OTEL telemetry | The agent itself, to the OTLP ports | Metrics and events: token counts, cost, model, tool activity. `cctrace init` labels them with the user ID, name, email, and team through `OTEL_RESOURCE_ATTRIBUTES`. |
| Session logs | The sync daemon, to `/api/sync` | Each line of the agent's session files as written on disk: prompts, replies, reasoning text, tool calls with their arguments and output. Each upload also carries project details: directory name, git remote URL, repository name, branch, and commit. |
| Rule files | The sync daemon | The CLAUDE.md and AGENTS.md files at the root of the git repositories your sessions run in |
| Usage-limit readings | The client | The plan's rate-limit usage as read on your machine |

`cctrace init` does not set `OTEL_LOG_USER_PROMPTS`, so Claude Code's own telemetry does not carry prompt text unless you set that variable yourself. Session logs carry the full conversation either way.

## Where it is stored

Collected data goes to the server you configured and into its TimescaleDB database. Other outbound requests:

- To read usage limits, the client queries Anthropic's usage API with the local Claude Code login.
- The server downloads OpenAI's public pricing and changelog pages to price Codex usage.

The weekly AI report feature, when enabled, sends data to the AI provider it is configured with. It is under active development and is not documented here.

Clients warn, but do not refuse, when an endpoint is plain `http://` to anything but the same machine (loopback), private addresses included. Put the server behind TLS before clients connect over a network you do not control; see [Install the server](../server/install.md#optional-https-with-caddy).

## Who can see what

| Data | `admin` | `user` |
|---|---|---|
| Session transcripts (`/sessions`) | Every user's | Their own |
| Rule files (`/rules`) | All repositories | Repositories where they have sessions |
| Aggregates: usage, cost, tools, plugins and skills, per-user lists | Everyone's | Everyone's |
| Telemetry rows (`/logs`) | Everyone's | Everyone's |
| User management, `/admin` | Yes | No |

- Aggregates are shared on purpose, so the whole team's usage adds up in one place.
- Attributes that carry free text (prompts, tool arguments and output, command text, stdout and stderr) are removed from telemetry rows before any dashboard response, for administrators too. They may still be stored.
- A `user` account sees its own sessions through its cctrace user ID. An account without one sees no session data (with the default `CCTRACE_USERID_ACCESS_CONTROL`). See [Users](../dashboard/users.md#how-data-is-attributed-to-an-account).

### `CCTRACE_USERID_ACCESS_CONTROL`

| Value | How a `user` account's sessions are matched |
|---|---|
| unset or anything but `false` (default) | By the account's cctrace user ID |
| `false` | By the account's sign-in email against the email the client uploaded with |

The setting changes only how sessions are matched to their owner. It does not make aggregates private.

## Redact on the client

Two profile options remove content from session logs before they leave your machine. Both are off by default.

```bash
cctrace config set options.redact_user_prompts true
cctrace config set options.redact_tool_details true
```

| Option | Replaces the values of these fields |
|---|---|
| `redact_user_prompts` | `content`, `text`, `thinking`, `summary`, `title` |
| `redact_tool_details` | `arguments`, `input`, `output`, `result`, `command`, `description`, `stdout`, `stderr` |

- Values are replaced with `[redacted by cctrace client]`, wherever the field appears in the record. A line that cannot be parsed is replaced as a whole.
- Token counts, cost, model, and timestamps are left alone, so dashboard figures do not change.
- Redaction covers session logs only. It does not touch OTEL telemetry, which the agent sends directly, or rule files.
- It applies to uploads made after the change. Records already on the server stay as they are.
- A running sync daemon keeps the options it started with. Restart it with `cctrace sync --stop` and then `cctrace sync --daemon`.

Add `--profile <name>` to act on a named profile. `cctrace status` lists the redaction that is on.

## Delete data

| What | Who | How |
|---|---|---|
| One session | Its owner (while the owner-delete policy allows it) or an administrator | Trash icon on `/sessions`. See [Pages](../dashboard/pages.md#delete-a-session). |
| A project's sessions, and future collection of it | Administrator | Session delete dialog or the project picker on `/sessions` |
| Everything stored under one account's email | Administrator | **Clear Collected Data** on `/users`. See [Users](../dashboard/users.md#manage-accounts). |

A deleted session is refused if a client uploads it again.

## Retention

| Table | Contents | Default |
|---|---|---|
| `otel_events` | Telemetry events | Dropped after 90 days, compressed after 30 |
| `otel_metrics` | Telemetry metrics | Dropped after 90 days, compressed after 30 |
| `session_records` | Session logs (conversation content) | No policy: kept until an operator sets one |

While `session_records` has no policy, the server logs this at startup:

```text
[cctraced] notice: session_records (conversation content) has no retention policy and is kept indefinitely, while otel_events/otel_metrics are dropped after 90 days. Set SESSION_RETENTION_DAYS (0 = keep forever) or choose an interval in Admin -> Storage.
```

Set retention on the **Storage** tab of `/admin`. `cctraced` also reads `SESSION_RETENTION_DAYS` and `OTEL_RETENTION_DAYS`, but the shipped compose file does not pass them to the container ([Configuration](../server/configuration.md#variables-the-compose-file-does-not-pass)). When one reaches the container, it wins and locks that setting in the dashboard. Shortening retention deletes existing data. Details: [Operations](../server/operations.md#data-retention).
