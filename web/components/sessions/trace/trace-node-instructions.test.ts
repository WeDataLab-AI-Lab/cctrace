import { describe, expect, it } from 'vitest';
import { extractTraceInstructions } from './trace-node-instructions';

describe('trace instruction source extraction', () => {
  it('preserves typed instruction role, source path, file path, and body state', () => {
    const entries = extractTraceInstructions({ state: 'observed', value: { instructions: [
      { kind: 'project', source_path: 'attachment.files', path: '/repo/AGENTS.md', body: { state: 'redacted', reason: 'source_redaction' } },
    ] } });

    expect(entries).toEqual([{ kind: 'project', source_path: 'attachment.files', path: '/repo/AGENTS.md', body: { state: 'redacted', reason: 'source_redaction' } }]);
  });

  it('reads nested AGENTS content blocks as text without inventing a file path', () => {
    const entries = extractTraceInstructions({ state: 'observed', value: { payload: { message: { content: [
      { type: 'text', text: '# AGENTS.md instructions' }, { type: 'text', text: 'Use the project rules.' },
    ] } } } });

    expect(entries).toEqual([{ kind: 'recorded', source_path: 'payload.message.content', body: { state: 'observed', value: '# AGENTS.md instructions\nUse the project rules.' } }]);
  });

  it('keeps legacy file paths and named instruction contexts distinct', () => {
    const entries = extractTraceInstructions({ state: 'observed', value: {
      attachment: { files: [{ path: '/repo/CLAUDE.md', content: 'Project rules' }] },
      payload: { developer_instructions: 'Developer rules' },
    } });

    expect(entries).toEqual([
      { kind: 'file', source_path: 'attachment.files', path: '/repo/CLAUDE.md', body: { state: 'observed', value: 'Project rules' } },
      { kind: 'developer', source_path: 'payload.developer_instructions', body: { state: 'observed', value: 'Developer rules' } },
    ]);
  });
});
