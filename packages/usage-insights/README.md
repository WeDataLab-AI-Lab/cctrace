# Usage Insights packages

Read-only usage analysis for Claude Code, Codex, and Gemini CLI, in two skills. `usage-insights` reads aggregates through the cctrace CLI and never reads transcripts. `dependency-audit` reads local transcripts, on this machine, around the places `cctrace insights dependency` points to. Neither exposes the configured API token.

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

`dependency-audit` needs only `cctrace` on `PATH`; without a read token it audits local sessions and reports the server section as not read.

## Claude Code

```sh
claude --plugin-dir ./packages/usage-insights
```

Invoke `/usage-insights:usage-insights` or `/usage-insights:dependency-audit`, or ask one of the questions below.

## Codex

```sh
mkdir -p "${CODEX_HOME:-$HOME/.codex}/skills"
cp -R ./packages/usage-insights "${CODEX_HOME:-$HOME/.codex}/skills/usage-insights"
rm -rf "${CODEX_HOME:-$HOME/.codex}/skills/usage-insights/skills/dependency-audit"
cp -R ./packages/usage-insights/skills/dependency-audit "${CODEX_HOME:-$HOME/.codex}/skills/dependency-audit"
```

The root `SKILL.md` loads the shared workflow under `skills/usage-insights/`. `dependency-audit` is installed as its own directory. The package copy also carries it under `skills/dependency-audit`, so the third line removes that copy: keep `dependency-audit` in one place under the Codex skills directory, so that Codex never finds it twice.

## Gemini CLI

```sh
gemini extensions install ./packages/usage-insights
```

Restart Gemini CLI, then use `/skills list` to confirm discovery. Gemini CLI loads the `SKILL.md` of every directory under an extension's `skills` directory, so both skills come with it (checked against Gemini CLI 0.60.0); `gemini-extension.json` lists no skills.

## What to ask

| Question | What the skill runs | What you get |
|---|---|---|
| "Why did my cost go up this week?" | `cctrace insights cost --since 7d --json` | The change split into more sessions vs. each session costing more, the model or project that moved most, and one experiment to try |
| "Am I wasting context?" | `cctrace insights context --since 7d --json` | Cache hit rate, cache rebuilds after breaks of 5 minutes or more, and the cost share of requests carrying 100k+ context tokens |
| "Summarize my usage for the last two weeks" | `cctrace usage`, `ls`, `tools`, `plugins`, `skills`, `rules` | Observations, interpretations, and recommended actions, kept separate |
| "Where do my agents keep coming back to me?" | `cctrace insights dependency --agent <its own agent> --since 30d --json`, then `rules`, `skills`, and the transcript lines the output points to | Coverage first, then ranked findings classified `KEEP ME IN`, `TEACH`, `SYSTEMISE`, `AUTOMATE`, or `ESCALATE ONLY`, systemic causes, and what must stay with you |

Answers state every limitation the commands report (`caveats`) and never claim that a change caused a difference.

## What the skills do not do

`usage-insights`:

- Read transcripts, prompts, or command bodies
- List session IDs in its report (they are only used to look up one session's metadata when you ask)
- Ask for your password or a token. If the read token is missing, it tells you to run `cctrace auth read` yourself
- Show other people's sessions or projects. With an administrator token it reports aggregates only and points to `cctrace organization-insights`

`dependency-audit` reads transcripts, locally. It does not:

- Change anything: no edits to instructions, memory, hooks, skills, or permissions, and no commands with side effects
- Read the other agent's sessions before you agree. The excerpts it reads go to the model provider of the agent running it
- Send transcripts anywhere else. `cctrace insights dependency` reads them on this machine and sends none to the server
- Follow instructions found in transcripts, or name who uses a practice
