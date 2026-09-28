// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { ProviderKeysSection } from './ai-provider-keys-section';
import type { AdminAICredential } from '@/lib/types';

const openaiCredential: AdminAICredential = {
  provider: 'openai',
  source: 'none',
  key_hint: null,
  env_var: 'CCTRACE_AI_OPENAI_API_KEY',
  reason: null,
  updated_at: null,
};

const renderSection = (credential: AdminAICredential = openaiCredential) => {
  // Retries would keep a failed mutation pending past the test; nothing here
  // reaches the network anyway, since only the form is opened and closed.
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
  render(
    createElement(QueryClientProvider, { client }, createElement(ProviderKeysSection, { credentials: [credential] })),
  );
};

const keyField = () => screen.queryByLabelText('OpenAI API 키');

afterEach(cleanup);

describe('ProviderKeysSection key form', () => {
  it('opens the form on the first press of the register button', async () => {
    renderSection();
    expect(keyField()).toBeNull();

    await userEvent.click(screen.getByRole('button', { name: 'API 키 등록' }));

    expect(keyField()).not.toBeNull();
  });

  it('closes the form on a second press, as 닫기 does', async () => {
    renderSection();
    const register = screen.getByRole('button', { name: 'API 키 등록' });

    await userEvent.click(register);
    await userEvent.click(register);

    expect(keyField()).toBeNull();
  });

  it('reopens the form on a third press', async () => {
    renderSection();
    const register = screen.getByRole('button', { name: 'API 키 등록' });

    await userEvent.click(register);
    await userEvent.click(register);
    await userEvent.click(register);

    expect(keyField()).not.toBeNull();
  });

  it('closes the form from the 닫기 button too', async () => {
    renderSection();

    await userEvent.click(screen.getByRole('button', { name: 'API 키 등록' }));
    await userEvent.click(screen.getByRole('button', { name: '닫기' }));

    expect(keyField()).toBeNull();
  });

  // The button is named for what it does, and a stored key is replaced rather
  // than registered; the toggle has to follow the button, not its label.
  it('toggles under the 교체 label when a key is already stored', async () => {
    renderSection({ ...openaiCredential, source: 'admin', key_hint: '7890' });
    const replace = screen.getByRole('button', { name: 'API 키 교체' });

    await userEvent.click(replace);
    expect(keyField()).not.toBeNull();

    await userEvent.click(replace);
    expect(keyField()).toBeNull();
  });

  // A key managed by an environment variable cannot be changed from the screen,
  // so the button stays disabled and no press opens the form.
  it('does not open the form while the key comes from the environment', async () => {
    renderSection({ ...openaiCredential, source: 'env', key_hint: '4321' });

    const register = screen.getByRole('button', { name: 'API 키 등록' });
    expect((register as HTMLButtonElement).disabled).toBe(true);
    await userEvent.click(register);

    expect(keyField()).toBeNull();
  });
});
