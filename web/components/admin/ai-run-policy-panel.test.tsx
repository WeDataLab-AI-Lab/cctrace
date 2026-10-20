// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AdminAIResponse, UpdateAdminAIRunPolicyRequest } from '@/lib/types';

const sent: UpdateAdminAIRunPolicyRequest[] = [];
// The first read answers; every refetch after a save stays in flight, the
// window in which an administrator can already reach the next control.
let reads = 0;

const adminAI: AdminAIResponse = {
  enabled: true,
  enabled_source: 'admin',
  env_enabled: null,
  runtimes: [],
  selected_runtime: '',
  runtime_source: 'default',
  selection_reason: null,
  selection_reason_code: null,
  secrets_reason: null,
  env_runtime: '',
  model: '',
  settings: null,
  schedule: { enabled: true, enabled_source: 'admin', weekday: 1, hour: 1, minute: 0, when_source: 'admin', tz: 'Asia/Seoul' },
  run_policy: {
    consent_required: false,
    consent_required_source: 'default',
    max_concurrent: 2,
    max_concurrent_source: 'default',
    max_concurrent_default: 2,
    max_concurrent_max: 10,
  },
  usage_this_week: { runs: 0, failed: 0, input_tokens: 0, output_tokens: 0 },
};

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>();
  return {
    ...actual,
    fetchAdminAI: () => {
      reads += 1;
      return reads === 1 ? Promise.resolve(adminAI) : new Promise<AdminAIResponse>(() => {});
    },
    fetchAdminAIModels: async () => ({ models: [] }),
    // Answers with the policy as the server would save it.
    updateAdminAIRunPolicy: async (req: UpdateAdminAIRunPolicyRequest) => {
      sent.push(req);
      return {
        run_policy: {
          ...adminAI.run_policy,
          ...(req.consent_required !== null && { consent_required: req.consent_required, consent_required_source: 'admin' as const }),
          ...(req.max_concurrent !== null && { max_concurrent: req.max_concurrent, max_concurrent_source: 'admin' as const }),
        },
      };
    },
  };
});

vi.mock('@/components/common/auth-context', () => ({
  useAuth: () => ({ isAdmin: true }),
}));

const { AITab } = await import('./ai-tab');

const renderTab = () => {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(AITab)));
};

beforeEach(() => {
  sent.length = 0;
  reads = 0;
});
afterEach(cleanup);

// The save replaces the whole policy, and a part the admin never set must stay
// unset: sending the shown default back would pin it, so a later change of the
// server's own limit would no longer reach this deployment.
describe('AITab run policy saves', () => {
  it('turns consent on and leaves the limit to the server', async () => {
    renderTab();
    await screen.findByLabelText('사용자 동의 받기');

    await userEvent.click(screen.getByLabelText('사용자 동의 받기'));

    expect(sent).toEqual([{ consent_required: true, max_concurrent: null }]);
  });

  it('sets the limit and leaves consent to the default', async () => {
    renderTab();
    await screen.findByLabelText('동시 실행 수');

    await userEvent.selectOptions(screen.getByLabelText('동시 실행 수'), '4');

    expect(sent).toEqual([{ consent_required: null, max_concurrent: 4 }]);
  });

  // The save replaces the whole policy, so a control that still read the policy
  // from before the last save would send that save's part back as unset.
  it('keeps the consent just saved when the limit changes before the refetch lands', async () => {
    renderTab();
    await screen.findByLabelText('사용자 동의 받기');

    await userEvent.click(screen.getByLabelText('사용자 동의 받기'));
    const select = screen.getByLabelText('동시 실행 수') as HTMLSelectElement;
    await waitFor(() => expect(select.disabled).toBe(false));
    await userEvent.selectOptions(select, '4');

    expect(sent).toEqual([
      { consent_required: true, max_concurrent: null },
      { consent_required: true, max_concurrent: 4 },
    ]);
  });

  it('offers every limit the server allows', async () => {
    renderTab();
    const select = await screen.findByLabelText('동시 실행 수');

    expect(Array.from((select as HTMLSelectElement).options, (o) => o.value)).toEqual(['1', '2', '3', '4', '5', '6', '7', '8', '9', '10']);
  });
});
