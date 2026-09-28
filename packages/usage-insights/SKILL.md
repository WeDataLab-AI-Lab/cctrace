---
name: usage-insights
description: Analyze a user's cctrace usage and work patterns from read-only CLI JSON. Use for personal usage summaries, period comparisons, high-cost sessions, and practical workflow recommendations; never use it to retrieve transcripts or credentials.
---

# Usage Insights

Use the same workflow as [the shared agent skill](skills/usage-insights/SKILL.md). It is bundled here so this directory can be installed as a Codex skill.

Run only these read-only commands:

```sh
cctrace usage --since 7d --json
cctrace usage --since 14d --until 7d --json
cctrace ls --since 7d --limit 20 --json
cctrace tools --since 7d --json
cctrace plugins --since 7d --json
cctrace skills --since 7d --json
cctrace rules --json
cctrace organization-insights --since 7d --json
cctrace insights cost --since 7d --json
cctrace insights context --since 7d --json
```

For "why did my cost go up" and "am I wasting context", follow the scenario procedures in the shared skill and report every entry in `caveats`.

Report observations, interpretations, and recommended actions separately. Never print, request, or expose API tokens. Never use `cctrace sessions`; it can contain transcript records.
