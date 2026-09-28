# Usage Insights packages

Read-only usage analysis for Claude Code, Codex, and Gemini CLI. The shared skill uses the cctrace CLI and never exposes the configured API token or transcript records.

## Before you install

- `cctrace` on `PATH`, set up with `cctrace init`
- A read token in the profile. Answer `Y` to the read token question in `cctrace init`, or run:

  ```sh
  cctrace auth read
  ```

  It asks for your dashboard password and stores the token as `server.read_token`. Administrator accounts are refused; they create a token under Settings > API Access Tokens and set it with `cctrace config set server.read_token <token>`.

Check that the CLI can read:

```sh
cctrace insights cost --json
```

## Claude Code

```sh
claude --plugin-dir ./packages/usage-insights
```

Invoke `/usage-insights:usage-insights`, or ask one of the questions below.

## Codex

```sh
mkdir -p "${CODEX_HOME:-$HOME/.codex}/skills"
cp -R ./packages/usage-insights "${CODEX_HOME:-$HOME/.codex}/skills/usage-insights"
```

The root `SKILL.md` loads the shared workflow under `skills/usage-insights/`.

## Gemini CLI

```sh
gemini extensions install ./packages/usage-insights
```

Restart Gemini CLI, then use `/skills list` to confirm discovery.

## What to ask

| Question | What the skill runs | What you get |
|---|---|---|
| "Why did my cost go up this week?" | `cctrace insights cost --since 7d --json` | The change split into more sessions vs. each session costing more, the model or project that moved most, and one experiment to try |
| "Am I wasting context?" | `cctrace insights context --since 7d --json` | Cache hit rate, cache rebuilds after breaks of 5 minutes or more, and the cost share of requests carrying 100k+ context tokens |
| "Summarize my usage for the last two weeks" | `cctrace usage`, `ls`, `tools`, `plugins`, `skills`, `rules` | Observations, interpretations, and recommended actions, kept separate |

Answers state every limitation the commands report (`caveats`) and never claim that a change caused a difference.

## What the skill does not do

- Read transcripts, prompts, or command bodies
- List session IDs in its report (they are only used to look up one session's metadata when you ask)
- Ask for your password or a token. If the read token is missing, it tells you to run `cctrace auth read` yourself
- Show other people's sessions or projects. With an administrator token it reports aggregates only and points to `cctrace organization-insights`
