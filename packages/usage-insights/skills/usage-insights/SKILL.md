---
name: usage-insights
description: Analyze a user's cctrace usage and work patterns from read-only CLI JSON. Use for personal usage summaries, period comparisons, high-cost sessions, and practical workflow recommendations; never use it to retrieve transcripts or credentials.
---

# Usage Insights

Use only the authenticated `cctrace` CLI. It reads the user's configured API token; never print, request, or expose API tokens, and never put a token in prompts, logs, or output.

Never use `cctrace sessions`, raw session records, prompt text, assistant text, command bodies, repository paths, or environment variables. Do not infer work type from unavailable content. Describe only observable tool, plugin, skill, rule, model, session, time, token, cost, and failure signals.

## Personal report

Use a seven-day window unless the user asks for another period. Fetch only the smallest JSON needed for the question:

```sh
cctrace usage --since 7d --json
cctrace usage --since 14d --until 7d --json
cctrace ls --since 7d --limit 20 --json
cctrace projects --json
cctrace tools --since 7d --json
cctrace plugins --since 7d --json
cctrace skills --since 7d --json
cctrace rules --json
```

Call `cctrace events --session SESSION_ID --json` only after the user asks to inspect a selected session or a high-cost/failure outlier. Event output is metadata-only; do not request a transcript as a substitute.

For comparisons, `--since 14d --until 7d` is the completed seven-day period immediately before the current seven days. State when a comparison cannot be made because one period lacks data.

## Administrator aggregate report

Use `cctrace organization-insights --since 7d --json` when it is available. It is an instance-wide aggregate report, not an organization tenancy feature. Do not emulate it with `usage --group-by user`, user filters, or per-user session queries.

Treat suppressed dimensions as unavailable. Never attempt to reconstruct them from other commands. The server returns a dimension only when at least five distinct users contributed to it.

## Scenario procedures

Prefer these commands over deriving the same numbers from raw lists. Each prints a small JSON summary computed by the CLI. Report every entry in `caveats`, and when `truncated` is true say which part of the window is missing. When `admin_scope`, `role_unknown`, or `multiple_users` is present, the rows may cover other people and session and project detail was withheld; do not try to recover it. An administrator token always reads every user, so point administrators to the administrator aggregate report rather than a personal analysis.

If a command says the profile has no read token, tell the user to run `cctrace auth read` themselves (it asks for their password); administrators create one under Settings > API Access Tokens instead. Never run it for them, and never ask them to paste a password or token.

### Why did my cost go up?

```sh
cctrace insights cost --since 7d --json
```

The output compares the last seven days with the seven days before them.

1. Lead with `delta_cost_usd` and `top_driver` (the named model or project whose cost moved most). Cost under `(unknown)` could not be attributed; say so rather than recommending a change for it. With `project_unavailable`, there is no project breakdown at all; use models only.
2. Use `dominant_effect` to say whether the change came from running more sessions (`volume`) or from each session costing more (`intensity`). When it is `undetermined`, there is no baseline to split against; report the totals only.
3. When `by_model` shows cost moving from one model to another, report the shift as a model-mix change, not as extra work. With `model_totals_differ`, compare models with each other, not with `delta_cost_usd`.
4. Describe `top_sessions` by start time, model, and cost, and offer to inspect one with `cctrace events --session SESSION_ID --json`. Session IDs are selection keys only: do not list them in the report, and do not fetch events unasked.
5. Recommend one reversible experiment aimed at the top driver, such as a cheaper default model for that project, and say which number to recheck next week.

The comparison is observational: never claim the named driver caused the change.

### Am I wasting context?

```sh
cctrace insights context --since 7d --json
```

Add `--project HASH`, with a value from `cctrace projects --json`, to scope the summary to one project.

- `overall.hit_rate` is cache-read tokens over all context tokens. A low rate means the agent keeps paying to resend context.
- `rebuilds` counts requests that wrote more cache than they read, at 20k context tokens or more, after the session sat idle for at least `min_gap_seconds` (the cache lifetime). They mean resuming a session whose cache had expired; recommend starting a fresh session or compacting before a break, and use `median_gap_seconds` to describe how long the breaks were.
- `bloated.cost_share` is the fraction of cost spent on requests carrying 100k context tokens or more. When it is large, recommend splitting long sessions or clearing context between unrelated tasks, and describe the sessions in `bloated_sessions` by time and size rather than by ID.

The thresholds are fixed estimates; present the results as signals, not as measured waste.

## Response contract

Separate observations, interpretations, and recommended actions:

1. **Observations** — measured values and explicit data limitations
2. **Interpretations** — cautious explanations grounded in those observations
3. **Recommended actions** — small, reversible workflow experiments and how to measure the next period

Do not score people, infer personal traits, claim causality from correlation, or label task types without an explicit user-provided label. Prefer recommendations such as tightening a verification step, reusing a successful skill, or reviewing repeated tool failures.
