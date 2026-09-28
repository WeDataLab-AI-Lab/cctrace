// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AdminAIResponse, UpdateAdminAIScheduleRequest } from '@/lib/types';

const sent: UpdateAdminAIScheduleRequest[] = [];

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
  schedule: { enabled: true, enabled_source: 'admin', weekday: 1, hour: 6, minute: 0, when_source: 'admin', tz: 'Asia/Seoul' },
  usage_this_week: { runs: 0, failed: 0, input_tokens: 0, output_tokens: 0 },
};

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>();
  return {
    ...actual,
    fetchAdminAI: async () => adminAI,
    fetchAdminAIModels: async () => ({ models: [] }),
    updateAdminAISchedule: async (req: UpdateAdminAIScheduleRequest) => {
      sent.push(req);
      return { schedule: adminAI.schedule };
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
});
afterEach(cleanup);

// Each control used to send only its own field, and the row write replaces the
// whole row: turning the switch on and then choosing a weekday cleared the
// switch again, so an administrator could never have both. The request has to
// carry the state the row should end up with.
describe('AITab weekly schedule saves', () => {
  it('sends the whole schedule when the weekday changes', async () => {
    renderTab();
    await screen.findByLabelText('요일');

    await userEvent.selectOptions(screen.getByLabelText('요일'), '3');

    expect(sent).toHaveLength(1);
    expect(sent[0]).toEqual({ enabled: true, weekday: 3, hour: 6, minute: 0 });
  });

  it('sends the whole schedule when the switch changes', async () => {
    renderTab();
    await screen.findByLabelText('주간 자동 생성');

    await userEvent.click(screen.getByLabelText('주간 자동 생성'));

    expect(sent).toHaveLength(1);
    expect(sent[0]).toEqual({ enabled: false, weekday: 1, hour: 6, minute: 0 });
  });
});

// The weekday and time are read in each user's zone, and for a user with none
// that is the server's default -- which used to be UTC with nothing on screen
// saying so. The panel names the zone.
describe('AITab weekly schedule zone', () => {
  it('names the zone the time is read in for users without one', async () => {
    renderTab();

    expect(await screen.findByText(/Asia\/Seoul/)).toBeTruthy();
  });
});
