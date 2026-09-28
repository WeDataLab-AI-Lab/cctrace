# Pages

What each dashboard page shows and who can use it.

Every page requires sign-in. A `user` account sees only its own sessions; aggregates are shared with every signed-in user. See [Privacy](../reference/privacy.md#who-can-see-what).

## Header

The header above every page holds an agent selector and an account selector. Pages that support them narrow their data to the selected agent and account.

## Sessions (`/sessions`)

A session list on the left and the selected session's transcript on the right.

- Select a session in the list to open its transcript. The list loads more as you scroll and shows how many sessions match out of the total.
- **Assembled** merges related session files (subagents, branches) into one conversation. **Raw** shows each file's records as they are, grouped by file.
- **Interactive** (the default) shows sessions with human turns, **Headless** shows machine-driven sessions such as `claude -p`, **All** shows both.
- The project picker narrows the list to one project. The search box filters the loaded sessions by session, user, or project.

### Delete a session

The trash icon in the transcript header (tooltip `세션 삭제`) deletes the session's records and the events and metrics attached to it. This cannot be undone, and the server refuses the session if a client uploads it again.

| Who | Can delete |
|---|---|
| `admin` | Any session. The dialog then offers to delete the project's other sessions and to stop collecting the project. |
| `user` | Their own sessions, while the owner-delete policy allows it (the default) |

Administrators set the owner-delete policy and remove projects from the blocked list in the gear dialog next to the trash icon (`세션 수집 설정`). They can also delete a whole project from the project picker.

## Users (`/users`)

The **Analytics** tab lists users by cost. Administrators also get the **Management** tab, described in [Users](users.md).

## Plugins & Skills (`/plugins`)

Calls to Claude Code slash commands and skills, and Codex skill injections, grouped by plugin or skill name.

- Period: **7d**, **30d** (default), **90d**, **All**.
- Summary cards: **Claude Calls**, **Codex Calls**, **Avg Tokens**, **Total**, **Input**, **Output** tokens.
- Views: **By Skill**, **By User**, **By Project**. **By Project** counts only sessions in git repositories.

`/skills` redirects here.

## Rules (`/rules`)

The CLAUDE.md and AGENTS.md files the client found in the git repositories your sessions ran in, with their version history.

- Filter by text, status (**Active**, **Missing**), and repository. Summary cards count **Repositories** and **Rule Files**.
- Select a file to read a version and add comments to it.
- A `user` account sees rule files only from repositories where it has sessions.

## Logs (`/logs`)

Raw telemetry from the last 30 days, 50 rows per page.

| Tab | Columns |
|---|---|
| Events | Time, Event, Model, Session, User, Input, Output, Cost, Age |
| Metrics | Time, Metric, Model, User, Value, Session, Age |

Logs are not limited to your own data. Attributes that carry free text, such as prompts and tool output, are removed before they reach the page.

## Admin (`/admin`)

Visible only to administrators.

| Tab | Shows |
|---|---|
| **Clients** | Each client that reported a version: Profile Email, Name, User ID, Client Version, Last Seen, Status |
| **Storage** | Retention, table sizes, and free space on the data volume. Retention is edited here; see [Operations](../server/operations.md#data-retention). |

`/versions` and `/storage` redirect to `/admin`.

The **Excluded Accounts**, **Unpriced Models**, **Insights**, and **AI** tabs are under active development and are not documented here.

## Settings (`/settings`)

- **Change Password**: enter the current password and a new one of at least 8 characters. An account with a temporary password is sent here until it changes it.
- **Appearance**: accent color, agent tone, and whether the logo mark is shown.

The API token and billing account sections are under active development and are not documented here.

## Pages under active development

These pages exist and are under active development. They are not documented here.

| Page | Sidebar entry |
|---|---|
| `/` | Overview |
| `/cost` | Usage |
| `/tools` | Tools |
| `/weekly` | Weekly report |
| `/open-api` | Open API |
