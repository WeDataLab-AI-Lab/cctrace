import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ToolsScopeNote } from './tools-scope-note';

// Codex tool counts come from the codex.tool.call metric (#698): what they cover
// differs from Claude's per-call events, and no failure detail exists for them.
describe('ToolsScopeNote', () => {
  it('states how Codex tool calls are counted and that their failures have no detail', () => {
    const markup = renderToStaticMarkup(createElement(ToolsScopeNote));

    expect(markup).toContain('Codex 도구 수는 OTEL 메트릭 기준');
    expect(markup).toContain('TUI·VSCode 실행만 집계');
    expect(markup).toContain('Codex 는 실패 상세 없음');
  });
});
