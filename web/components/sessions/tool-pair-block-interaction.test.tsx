// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { ToolPairBlock } from './tool-pair-block';

afterEach(cleanup);
it('identifies a collapsed tool by its command instead of its output', () => {
  render(<ToolPairBlock defaultOpen={false} pair={{ name: 'Bash', input: '{"command":"go test ./internal/sessiontrace","description":"verify trace"}', result: 'hundreds of output lines', isError: false, startTs: '' }} />);
  expect(screen.getByRole('button', { name: /Bash/ }).textContent).toContain('go test ./internal/sessiontrace');
  expect(screen.getByRole('button', { name: /Bash/ }).textContent).not.toContain('hundreds of output lines');
});
it('shows the Codex cmd field without the surrounding JSON', () => {
  render(<ToolPairBlock defaultOpen={false} pair={{ name: 'exec_command', input: '{"cmd":"go test ./internal/sessiontrace"}', result: 'ok', isError: false, startTs: '' }} />);
  const button = screen.getByRole('button', { name: /exec_command/ });
  expect(button.textContent).toContain('go test ./internal/sessiontrace');
  expect(button.textContent).not.toContain('"cmd"');
});
it('starts compact and opens an exact tool inspection without losing the inline contents', () => {
  const inspect = vi.fn();
  render(<ToolPairBlock defaultOpen={false} pair={{ id: 'call-2', name: 'Read', input: '{"path":"a.go"}', result: 'contents', isError: false, startTs: '2026-10-17' }} onInspect={inspect} />);
  expect(screen.queryByText('contents', { selector: 'pre' })).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: /Read/ }));
  expect(screen.getByText('contents', { selector: 'pre' })).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: '이 툴 호출 검사' }));
  expect(inspect).toHaveBeenCalledOnce();
});
