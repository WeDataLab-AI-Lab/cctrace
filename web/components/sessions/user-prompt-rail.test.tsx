// @vitest-environment jsdom
import { createElement } from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { UserPromptRail } from './user-prompt-rail';
import type { UserPrompt } from './session-utils';

afterEach(cleanup);

const prompts: UserPrompt[] = [
  { anchorId: 'turn-18', key: 'turn-18', text: '정책 설치 실패 원인', ts: '2026-10-20T01:32:14Z', requestNo: 18 },
  { anchorId: 'turn-19', key: 'turn-19', text: 'SHA 대조', ts: '2026-10-20T01:33:00Z', requestNo: 19 },
];

it('keeps conversation and execution as two distinct actions on one request row', () => {
  const onJump = vi.fn();
  const onOpenTrace = vi.fn();
  render(createElement(UserPromptRail, { prompts, onJump, onOpenTrace }));
  fireEvent.click(screen.getByRole('button', { name: /정책 설치 실패 원인/ }));
  expect(onJump).toHaveBeenCalledWith('turn-18');
  expect(onOpenTrace).not.toHaveBeenCalled();
  fireEvent.click(screen.getAllByRole('button', { name: '실행' })[1]);
  expect(onOpenTrace).toHaveBeenCalledWith('turn-19');
  expect(onJump).toHaveBeenCalledTimes(1);
});

it('shows the whole-session request number and omits execution when unavailable', () => {
  const { container } = render(createElement(UserPromptRail, { prompts, onJump: vi.fn() }));
  expect(container.textContent).toContain('18');
  expect(screen.queryByRole('button', { name: '실행' })).toBeNull();
});
