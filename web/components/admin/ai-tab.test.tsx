import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { AITab } from './ai-tab';
import { ADMIN_AI_KEY } from '@/components/admin/ai-account-section';
import type { AdminAIResponse, AdminAIRuntime } from '@/lib/types';

const runtime = (over: Partial<AdminAIRuntime> & Pick<AdminAIRuntime, 'key'>): AdminAIRuntime => ({
  implemented: true,
  configured: false,
  available: false,
  reason: null,
  selected: false,
  provider: '',
  auth_mode: '',
  account_email: '',
  plan_type: '',
  used_percent: null,
  personal_account_warning: false,
  credential: null,
  ...over,
});

const queryMock = vi.hoisted(() => ({ ai: null as unknown }));

queryMock.ai = {
  enabled: false,
  enabled_source: 'default',
  env_enabled: null,
  runtimes: [
    runtime({ key: 'openai-api', configured: true, available: true, selected: true, provider: 'openai', auth_mode: 'api_key' }),
    // Not implemented: the screen must leave this one out of the options.
    runtime({ key: 'nvidia-api', implemented: false, provider: 'nvidia' }),
    runtime({ key: 'litellm-api', provider: 'litellm' }),
  ],
  selected_runtime: 'openai-api',
  runtime_source: 'admin',
  selection_reason: null,
  selection_reason_code: null,
  secrets_reason: null,
  env_runtime: '',
  model: 'gpt-5.6-terra',
  settings: null,
  // The built-in default, off: these tests are about runtime choice, not schedules.
  schedule: { enabled: false, enabled_source: 'default', weekday: 1, hour: 6, minute: 0, when_source: 'default', tz: '' },
  usage_this_week: { runs: 1, failed: 0, input_tokens: 1000, output_tokens: 200 },
} satisfies AdminAIResponse;

vi.mock('@tanstack/react-query', () => ({
  useQuery: ({ queryKey }: { readonly queryKey: readonly string[] }) =>
    queryKey[0] === 'admin-ai'
      ? { data: queryMock.ai, isLoading: false, isError: false }
      : { data: undefined, isLoading: false, isError: false },
  useMutation: () => ({ mutate: () => {}, reset: () => {}, isPending: false, error: null }),
  useQueryClient: () => ({ invalidateQueries: () => {}, setQueryData: () => {} }),
}));

vi.mock('@/components/common/auth-context', () => ({
  useAuth: () => ({ isAdmin: true }),
}));

// The mock above routes on the literal 'admin-ai' rather than on ADMIN_AI_KEY,
// because a vi.mock factory is hoisted above the imports and cannot reference
// one; pulling the constant in with a dynamic import inside the factory would
// import ai-account-section, which imports the very module being mocked. So the
// literal is a copy of a value owned by ai-account-section, and this asserts the
// copy still matches. Rename the query key without this and the mock falls
// through to the catalog branch, AITab renders with data: undefined, and the
// failure shows up as an empty panel somewhere else instead of here.
describe('the react-query mock', () => {
  it('routes on the key AITab actually queries with', () => {
    expect(ADMIN_AI_KEY[0]).toBe('admin-ai');
  });
});

const renderTab = (): string => renderToStaticMarkup(createElement(AITab));

describe('AITab runtime options', () => {
  it('leaves out a runtime this build cannot drive', () => {
    const markup = renderTab();

    expect(markup).not.toContain('NVIDIA API');
    expect(markup).not.toContain('nvidia-api');
  });

  it('offers the runtimes this build can drive', () => {
    const markup = renderTab();

    expect(markup).toContain('OpenAI API');
    expect(markup).toContain('LiteLLM API');
  });

  it('keeps the rest of the panel intact', () => {
    const markup = renderTab();

    expect(markup).toContain('런타임');
    expect(markup).toContain('이번 주 사용량 (UTC)');
  });
});

// The default every user follows until they change it. An administrator has to
// see what turning it on would do before turning it on, so the weekday and time
// show even while it is off.
describe('AITab weekly schedule', () => {
  it('shows the default firing and its switch', () => {
    const markup = renderTab();

    expect(markup).toContain('주간 자동 생성');
    expect(markup).toContain('월요일');
    expect(markup).toContain('06:00');
  });

  // Asserted on the panel's own switch, not on the word "꺼짐": the AI report
  // switch above it already prints that for this fixture, so a looser check
  // passes with no schedule panel on the screen at all.
  it('carries its own switch, separate from the AI report switch', () => {
    expect(renderTab()).toContain('aria-label="주간 자동 생성"');
  });
});
