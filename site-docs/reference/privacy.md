# Privacy

What cctrace collects, where it keeps it, who can read it, and how long it stays.

!!! warning "The server stores conversation content"
    Session logs include what people typed, the agent's replies, and tool commands and output. By default they are kept until an operator sets a retention period. Decide on retention and redaction before you connect other people.

## What is collected

| Data | Sent by | Contents |
|---|---|---|
| OTEL telemetry | The agent itself, to the OTLP ports | Metrics and events: token counts, cost, model, tool activity. `cctrace init` labels them with the user ID, name, email, and team through `OTEL_RESOURCE_ATTRIBUTES`. |
| Session logs | The sync daemon, to `/api/sync` | Each line of the agent's session files as written on disk: prompts, replies, reasoning text, tool calls with their arguments and output. Each upload also carries project details: directory name, git remote URL, repository name, branch, and commit. |
| Session snapshots and the collector heartbeat | The sync daemon, to `/api/session-observations` | For each Claude Code session, from its metadata lines: the AI title, the custom title and the agent name; the cost total and the lines added and removed of each run; the model ids used; the PR links it recorded; and the ids of the sessions it continued in. Also counts of the files the session edited: how many there are, how many `git status` still reports as uncommitted, how many repositories they lie in, how many git could not place, when the counts last changed, and whether they miss edits the client never read; never the paths. Also verified git-call commit identities and optional subjects, with unresolved call reasons (described below). For each Codex session, from its rollout file, each turn's id and the id of the turn it belongs under, when it started and ended, whether it completed, was aborted (with the reason Codex gives) or ended in an error (with the error's kind, never its message), its duration and time to first token, the model context window, the collaboration mode, the model and the reasoning effort. With them, a heartbeat saying the collector is running. |
| Live session status and session ends | The sync daemon, to `/api/session-observations` | For each running Claude Code session process, read about once a minute from the state file Claude Code keeps for it (`~/.claude/sessions/<pid>.json`): the session's status (busy, idle, waiting, shell, or gone once the process has ended), what a waiting session waits for (input, permission, dialog, sandbox, worker, goal or other), the session's name and where the name came from, the process id and start time, when the status last changed, and the session's project. When a session ends, the reason the SessionEnd hook gives, such as `clear` or `logout`, and the session's project. The project is the session's project directory name, which spells out the path of its working directory. On Windows Claude Code writes no `procStart` to the state file, so no live status is sent from Windows. |
| Rule files | The sync daemon | The CLAUDE.md and AGENTS.md files at the root of the git repositories your sessions run in |
| Usage-limit readings | The client | The plan's rate-limit usage as read on your machine |
| Policy request | The client, to `/api/client-config-policy` | Asked by each policy check: the installation ID (a random UUID that `cctrace` creates under `~/.cctrace` the first time a policy command runs) and the profile name, as query parameters, with no token. The server answers from its policy file and stores nothing from the request. Its access log keeps the method, path, status, duration and the caller's address, not the query. |
| Policy target report | The client, to `/api/client-policy-report` | For a profile that is logged in, one record after each `cctrace config policy apply` run, whatever the run found: the installation ID and profile name (empty for the default profile), the email attached to the profile's uploaded records and the user ID, the server and OTLP addresses the profile uses and its OTLP protocol, the number of the last policy it applied, how the run ended (`ok`, `deferred` or `failed`), the number of the policy a failed or deferred run had reached, and the client's own error text. The error text can name local file paths and other profiles, and is cut to 500 characters; a path of a profile's explicit Codex home is replaced by `[explicit Codex home]`. An `ok` run can carry text too, until the next policy: when a policy leaves the default Codex configuration as it found it, the text names that file's absolute local path, and when it leaves an explicit Codex home's configuration as it found it, the text is `explicit Codex home kept`, with no path. The request headers add the client version, OS and architecture; the server stores only the version. |
| Client version record | The client, with each `/api/sync` upload | For each account email: the user ID, the version, OS and architecture from the request headers, the time of the last sync and, for a client that reports it, its self-update state: the version it fails to reach, the number of consecutive failures, the times of the first and last failure, and the error text (it can name local file paths and is cut to 500 characters). Account exclusion and `options.collect_repository_prefixes` do not stop it. |

`cctrace init` does not set `OTEL_LOG_USER_PROMPTS`, so Claude Code's own telemetry does not carry prompt text unless you set that variable yourself. Session logs carry the full conversation either way.

A session snapshot is attributed to the billing account of the newest record the client has read from that session. If the account changes partway through a session, the whole snapshot goes under the newer account, and an account exclusion is judged by that account. A Codex session's snapshot goes under the account signed in to the Codex home its rollout file is in, as of the newest turn line the client has read, and is sent only by a sync that reads that Codex home.

To count a session's uncommitted files, the client finds on your machine the repository each file the session edited with Edit, Write, MultiEdit or NotebookEdit lies in, by the nearest `.git` above it, checks with `git rev-parse` where git puts that repository's work tree, and runs `git status` there, asking about those files alone. It keeps those file paths in its sync state, counts again about once an hour while the session has been active in the last seven days, sends the counts when they change, and once those seven days have passed drops the paths and keeps the last counts. With `options.collect_repository_prefixes` set, it reads each such repository's origin to judge it, `git status` runs only in the repositories it collects, and a file in any other repository, or in none, is not counted. No git runs for a session whose account is excluded. Codex sessions get no such counts.

A live status is attributed to the account signed in to that Claude home when the status is read, and a session end to the one signed in when the session ended. Neither is sent for an excluded account or for a repository outside `options.collect_repository_prefixes`. The SessionEnd hook's transcript path and working directory stay on your machine; only the project they belong to is sent. Until a sync sends it, a session end waits in a `pending` directory beside the profile's sync state (`~/.cctrace/pending/` for the default profile), for at most seven days.

For Claude Code git calls, the client also reads up to 2,000 entries of the allowed work tree's HEAD reflog and its current branch. Only a creation candidate named by that call's output hash is attached to the snapshot: full hash, commit time, parent count, kind, repository, recent branch, call id, transcript identifier, proof, optional amend predecessor, and a subject of at most 200 bytes. Time or a matching subject alone is not proof. Unresolved calls carry only their id, transcript identifier, time and reason. Commit items and unresolved calls each have a 100-entry limit within the snapshot's 60 KiB limit; lost coverage is marked truncated. Commands, output text and unverified candidate subjects are not saved in the call state. Pending calls keep directories and hash evidence locally, and expire after seven days with an unresolved reason. Commit lookup uses at most 2.5 seconds of the same five-second flush budget used for uncommitted counts. Excluded accounts and repositories are not inspected for commit subjects.

The policy request and the policy target report belong to signed server policies, which only a client that links an update public key or a policy public key can use. Builds from this repository without an update or policy public key link empty keys (see [Overview](../index.md)). Such a client never starts the background policy check and never sends a policy request. `cctrace config policy apply` ends with an error that begins "policy public key is not configured" and, for a profile that is logged in, still sends one report of that failed run each time you run it. A client that does link a key runs the background check when a watch process starts, about every five minutes while it runs, and at the end of a one-shot `cctrace sync`; each run ends with a report from a profile that is logged in. A profile that is not logged in sends no report, and neither do `cctrace config policy status` and `cctrace config policy check`.

## Where it is stored

Collected data goes to the server you configured and into its TimescaleDB database. Other outbound requests:

- To read usage limits, the client queries Anthropic's usage API with the local Claude Code login.
- The server downloads OpenAI's public pricing and changelog pages to price Codex usage.
- A client that links an update public key or a policy public key tests a policy it received before applying it (`cctrace config policy check` and `apply`): small requests to the policy's new server and OTLP addresses, where the OTLP requests carry the profile's upload token.

A policy target report is kept as one row per installation ID and profile: a new report replaces the row's values and keeps the time the row was first seen, and an empty version header does not erase the version stored before. A client version record is kept as one row per account email, updated by each sync. The server checks the form of a report, not whether it is true: a logged-in client can report any installation ID and profile. A report with a malformed or too long field is refused with HTTP 400 and the reason is logged (see [Operations](../server/operations.md#logs)).

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
- Client version records are shown to administrators only, on the **Clients** tab of [`/admin`](../dashboard/pages.md#admin-admin).
- Policy target reports have no dashboard page and no API that reads them. They can be read only in the database.

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
- Redaction covers session logs, the titles in session snapshots and the session names in live status. It does not touch OTEL telemetry, which the agent sends directly, rule files, policy target reports or client version records.
- It applies to uploads made after the change. Records already on the server keep what they hold; [re-enrichment](#re-enrichment) can add fields to them.
- A running sync daemon keeps the options it started with. Restart it with `cctrace sync --stop` and then `cctrace sync --daemon`.

Some content in Claude Code session lines sits under names that also mean something else, so it is found by line kind and position rather than by field name. Each kind of content in this table gets the same treatment in every place the table names:

| Content | `redact_user_prompts` | `redact_tool_details` |
|---|---|---|
| Questions and options asked through AskUserQuestion, and the plan ExitPlanMode presents (tool `input`) | Hidden | Hidden |
| Answers to AskUserQuestion (the `answers` field of `toolUseResult`, and `annotations`) | Hidden | Hidden |
| Tool output inside `tool_result` blocks (`content`) | Hidden | Hidden |
| A prompt typed while the agent was busy (`queued_command` attachment, `prompt`) | Hidden | Kept |
| A background task's result: queued (`queued_command` with `commandMode` `task-notification`) and as its own user line (an `origin` of kind `task-notification`, the `content` of `message`) | Hidden | Hidden |
| Plan file text (`plan_file_reference`, `planContent`) | Hidden | Hidden |
| Plan file paths (`plan_file_reference` and `plan_mode`, `planFilePath`) | Kept | Hidden |
| Task subjects (`task_reminder` attachment, `content`) | Hidden | Hidden |
| Claude Code's recap (`system` line with subtype `away_summary`, `content`) | Hidden | Hidden |
| The summary a session continued after compaction opens with (user line with `isCompactSummary`, the `content` of `message`) | Hidden | Hidden |
| Verified commit subjects in session snapshots | Hidden; subjects read while either option is on stay hidden after it is turned off | Hidden; also applied again when sending |
| Session titles and agent names in session snapshots (`/api/session-observations`) | Hidden; a title first read while the option was on stays hidden after it is turned off | Kept |
| Session names in live session status (`/api/session-observations`) | Hidden; unlike a title, a name is read afresh every minute and goes as the option says when it is sent, so once the option is turned off a session's current name goes in plain text | Kept |

Fields that record what happened rather than what was said, such as prompt ids, permission mode, turn durations and counts, task status, and the model in use, are not redacted. Neither option hides commit hashes, times, kinds, parent counts, repositories, branches, call identifiers or unresolved reasons, or the PR links, cost and line totals, model ids, continued-in session ids, edited and uncommitted file counts, or Codex turn times, endings and error kinds, models and efforts in session snapshots, nor a live session's status, what it waits for, its process id and start time, or the reason a session ended.

With `redact_tool_details` alone, some tool output stays, because Claude Code writes it into a user line as conversation text rather than under a tool field. These gaps are older than the rules above; `redact_user_prompts` removes all of them:

- The tool results an SDK harness writes as text, such as `Tool result (Bash, id=…)`
- The output of a `!` shell command (`<bash-stdout>`)
- The output of a local slash command (`<local-command-stdout>`)

Add `--profile <name>` to act on a named profile. `cctrace status` lists the redaction that is on.

### Re-enrichment

`cctrace sync reenrich` reads session files again from the start and adds the fields a newer client collects to records already on the server. It runs only when you type it.

- It sends only the fields it can add, never the conversation itself (`message`). By default those are the fields that record what happened. Answers, queued prompts and background task results, plan files and their paths, task subjects, and recaps stay on your machine.
- `--include-content` sends and adds those too, under the redaction options in force when you run it. A record collected while redaction was on, before these fields were collected, gains them in plain text if redaction is now off. The command says so before it sends anything.
- A value a record already holds is never replaced, a `[redacted by cctrace client]` marker included. Conversation text is never added or changed.
- It leaves alone the part of a file that was already on disk when sync first ran, which sync never collected. For a file sync once found shrunk, it also leaves alone everything before the size the file had then. That part can include lines sync did collect, so those records are not re-enriched.
- Lines sync consumed without collecting them, because their repository was outside `options.collect_repository_prefixes`, are left alone too, together with everything before them; the session snapshot rebuild skips the same part. As a side effect, narrowing the allowlist and widening it again leaves out of re-enrichment the lines collected before it was narrowed.
- It also builds each session's snapshot (see [What is collected](#what-is-collected)) from the same part of the file, and sends the snapshots before it exits. By default the snapshot leaves out the titles and the agent name; `--include-content` includes them, under the redaction options in force then. A snapshot without titles does not take the place of one this client has sent with titles.
- With Codex sync on, it builds each Codex session's turns the same way, from the part of its rollout file sync collected, and sends them in the same flush.
- For a session active in the last seven days, it also reads from the same part the files the session edited, so its uncommitted count covers the whole session rather than what was read since this client. The paths stay on your machine.

- Git call backfill uses that same collected portion without a seven-day age cutoff. By default verified commits omit the subject key; `--include-content` permits subjects under both current redaction policies. A subject or redaction marker already present is not replaced by re-enrichment.

## Local analysis

`cctrace local serve`, and the local service that `cctrace local enable` turns on, read the Claude Code and Codex session files on your machine and show them in the dashboard without a sync. See [Local analysis](../client/local.md).

- The service listens only on a loopback address, refuses browser requests from any origin you did not name, and serves session data only to requests that carry its token.
- It makes no outbound request. The dashboard page reads it directly from your browser, without cookies, so this path sends nothing to the server.
- `serve` makes a new token on each run and prints it once. `enable` prints the token once and keeps only its SHA-256 hash, in the profile's directory under `~/.cctrace`. `disable` discards the hash.
- If you choose "이 탭에 연결 저장" (remember the connection in this tab) when you connect, the browser keeps the service address and the token in `sessionStorage` until the tab closes. The dashboard page never writes the token to its address, to a server request or to a log.

## Delete data

| What | Who | How |
|---|---|---|
| One session | Its owner (while the owner-delete policy allows it) or an administrator | Trash icon on `/sessions`. See [Pages](../dashboard/pages.md#delete-a-session). |
| A project's sessions, and future collection of it | Administrator | Session delete dialog or the project picker on `/sessions` |
| Telemetry, session logs, live state, policy target reports, the client version record, the latest usage-limit reading and the settings that hide the account's sessions from AI session records (the overview, review items, work status and tags an AI model writes for each session), stored under one account's email (the rows named below the table stay) | Administrator | **Clear Collected Data** on `/users`. See [Users](../dashboard/users.md#manage-accounts). |

A deleted session is refused if a client uploads it again.

Merging accounts moves an account's policy target reports and its client version record to the account it is merged into. When both accounts have a client version record, the two are combined the way a sync updates one: the record synced last wins, but where it lacks the version, OS, architecture or self-update state, the other record's is kept. The settings that hide the account's sessions from AI session records move too, and the target account sees them as its own. Settings saved under a cctrace user ID stay where they are and keep applying through that ID. Where the target already has the same setting, the target's stays. A moved setting that covers the whole account hides the target account's own sessions as well. The account's latest usage-limit reading is deleted, not moved: the target's own reports keep its reading current.

**Clear Collected Data** also deletes the settings that hide the account's sessions from AI session records. Sessions collected for the account afterwards are therefore no longer hidden from them until the settings are set again through `/api/privacy`.

Neither **Clear Collected Data** nor merging accounts removes rule file versions, which record the account's email as their author. Nor do they remove or move the history of the account's usage-limit readings (`quota_samples`) and the list of excluded addresses derived from it, or the hourly usage aggregates older than 48 hours that name it. Those rows can stay in the database until an operator deletes them there.

A merge leaves one latest usage-limit reading for both profiles. If they were signed in to different Claude accounts, their later reports are stored under the target's address and replace each other in turn. The history of readings does not follow the merge: each reading keeps the name of the profile that reported it.

## Retention

| Table | Contents | Default |
|---|---|---|
| `otel_events` | Telemetry events | Dropped after 90 days, compressed after 30 |
| `otel_metrics` | Telemetry metrics | Dropped after 90 days, compressed after 30 |
| `session_records` | Session logs (conversation content) | No policy: kept until an operator sets one |
| `client_policy_reports` | Policy target reports | Not covered by retention settings: kept until **Clear Collected Data** or an operator deletes the rows |
| `client_versions` | Client version records | Not covered by retention settings: kept until **Clear Collected Data** or an operator deletes the rows |

While `session_records` has no policy, the server logs this at startup:

```text
[cctraced] notice: session_records (conversation content) has no retention policy and is kept indefinitely, while otel_events/otel_metrics are dropped after 90 days. Set SESSION_RETENTION_DAYS (0 = keep forever) or choose an interval in Admin -> Storage.
```

Set retention on the **Storage** tab of `/admin`. `cctraced` also reads `SESSION_RETENTION_DAYS` and `OTEL_RETENTION_DAYS`, but the shipped compose file does not pass them to the container ([Configuration](../server/configuration.md#variables-the-compose-file-does-not-pass)). When one reaches the container, it wins and locks that setting in the dashboard. Shortening retention deletes existing data. Details: [Operations](../server/operations.md#data-retention).
