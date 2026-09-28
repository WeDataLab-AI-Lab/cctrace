/** What every Codex tool count rests on (#698). Codex sends no per-call event:
 *  its counts come from the codex.tool.call metric, which counts calls made
 *  inside a code-mode exec script and is sent only by TUI and VS Code runs.
 *  Shared so the Tools and Weekly pages cannot describe it differently; each
 *  page appends what the metric's shape costs that page. */
const CODEX_TOOL_SCOPE = 'Codex 도구 수는 OTEL 메트릭 기준 — 코드 모드 내부 호출 포함, TUI·VSCode 실행만 집계';

export { CODEX_TOOL_SCOPE };
