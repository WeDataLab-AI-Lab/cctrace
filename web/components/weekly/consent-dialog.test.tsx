import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { AIConsentInfo } from '@/lib/types';
import { ConsentDisclosure } from './consent-dialog';

const info: AIConsentInfo = {
  runtime_key: 'codex-app-server:chatgpt',
  runtime: 'openai-api',
  provider: 'OpenAI',
  disclosure_version: '2026-09-14',
  granted: false,
  granted_at: null,
  sends: ['서버가 준 보내는 항목 A', '서버가 준 보내는 항목 B'],
  not_sends: ['서버가 준 보내지 않는 항목'],
  provider_retention: '확인되지 않음',
};

describe('ConsentDisclosure', () => {
  // The lists come from the server per runtime; nothing is hard-coded (spec §4).
  it('shows exactly what the server says is sent and not sent', () => {
    const html = renderToStaticMarkup(createElement(ConsentDisclosure, { info }));

    expect(html).toContain('codex-app-server:chatgpt');
    expect(html).toContain('공급자: <span class="text-ink">OpenAI</span>');
    expect(html).toContain('보내는 것');
    expect(html).toContain('서버가 준 보내는 항목 A');
    expect(html).toContain('서버가 준 보내는 항목 B');
    expect(html).toContain('보내지 않는 것');
    expect(html).toContain('서버가 준 보내지 않는 항목');
    expect(html).toContain('공급자 보관');
    expect(html).toContain('확인되지 않음');
    expect(html.match(/<li/g)).toHaveLength(3);
  });

  it('adds nothing of its own to the lists', () => {
    const html = renderToStaticMarkup(createElement(ConsentDisclosure, { info: { ...info, sends: [], not_sends: [] } }));

    expect(html).not.toContain('<li');
  });
});
