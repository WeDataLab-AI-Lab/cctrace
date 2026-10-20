// @vitest-environment jsdom
import { mount, waitFor } from './page-test-fixture';
import { fireEvent, screen } from '@testing-library/react';
import { expect, it, vi } from 'vitest';

it('gives a narrow viewport to the conversation, then opens the session list as a separate step', async () => {
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() }));
  mount(undefined, false, true);
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('working-row'));
  expect(document.getElementById('session-navigation')?.hidden).toBe(true);
  fireEvent.click(screen.getByRole('button', { name: '세션 목록 펼치기' }));
  expect(document.getElementById('session-navigation')?.hidden).toBe(false);
  expect(document.getElementById('session-detail')?.hidden).toBe(true);
  fireEvent.click(screen.getByRole('button', { name: /other-ro/ }));
  expect(document.getElementById('session-navigation')?.hidden).toBe(true);
  expect(document.getElementById('session-detail')?.hidden).toBe(false);
  expect(screen.getByTestId('viewer-session').textContent).toBe('other-row');
});
