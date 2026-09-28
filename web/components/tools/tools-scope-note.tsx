import { CODEX_TOOL_SCOPE } from '@/lib/codex-tool-scope';

/** What the tool figures rest on (#698). A metric has no individual call, so
 *  Recent Failures lists Claude's alone. */
const ToolsScopeNote = () => (
  <p className="text-[11.5px] text-ink-3">
    {`${CODEX_TOOL_SCOPE} · Codex 는 실패 상세 없음 (Recent Failures 는 Claude 도구만)`}
  </p>
);

export { ToolsScopeNote };
