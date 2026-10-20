// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { SessionPersonalEditor } from './session-personal-editor';

const fetchPersonalSession = vi.hoisted(() => vi.fn());
const savePersonalSession = vi.hoisted(() => vi.fn());
vi.mock('@/lib/personal-state-api', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/lib/personal-state-api')>(),
  fetchPersonalSession, savePersonalSession,
}));

afterEach(() => { cleanup(); fetchPersonalSession.mockReset(); savePersonalSession.mockReset(); });

const mount = () => {
  fetchPersonalSession.mockResolvedValue({ revision: 3, classification: 'unsorted', note: null });
  savePersonalSession.mockResolvedValue({ revision: 4, classification: 'done', note: null });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onClassificationSaved = vi.fn();
  render(createElement(QueryClientProvider, { client }, createElement(SessionPersonalEditor, {
    sessionId: 'session-1', userId: 7, onClassificationSaved,
  })));
  return { onClassificationSaved };
};

it('offers only the classification, without the removed notes UI', async () => {
  mount();
  await userEvent.click(screen.getByRole('button', { name: '분류' }));
  expect(screen.getByRole('combobox')).toBeTruthy();
  expect(screen.queryByRole('textbox')).toBeNull();
  expect(screen.queryByText(/메모/)).toBeNull();
});

it('saves a classification change and reports it', async () => {
  const { onClassificationSaved } = mount();
  await userEvent.click(screen.getByRole('button', { name: '분류' }));
  await screen.findByRole('combobox');
  await userEvent.selectOptions(screen.getByRole('combobox'), 'done');
  await userEvent.click(screen.getByRole('button', { name: '저장' }));
  await screen.findByText(/저장됨|저장 중/);
  expect(savePersonalSession).toHaveBeenCalledWith('session-1', expect.objectContaining({ classification: 'done', expected_revision: 3 }));
  expect(onClassificationSaved).toHaveBeenCalledWith('session-1', 'done');
});
