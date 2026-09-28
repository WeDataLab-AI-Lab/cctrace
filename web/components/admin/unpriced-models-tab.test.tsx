import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { UnpricedModelsTab } from './unpriced-models-tab';

const queryMock = vi.hoisted(() => ({
  data: {
    unpriced: [
      {
        agent: 'codex',
        model: 'codex-auto-review',
        rows: 1002,
        input_tokens: 17_000_000,
        output_tokens: 756_851,
        cache_read_tokens: 0,
        first_ts: '2026-08-05T00:00:00Z',
        last_ts: '2026-08-18T00:00:00Z',
      },
      {
        agent: 'codex',
        model: '',
        rows: 3,
        input_tokens: 10,
        output_tokens: 1,
        cache_read_tokens: 0,
        first_ts: '2026-08-05T00:00:00Z',
        last_ts: '2026-08-05T00:00:00Z',
      },
    ],
    flat_rate: [
      {
        agent: 'codex',
        model: 'gemma4:12b',
        reason: 'local ollama',
        created_by: 'admin@example.com',
        created_at: '2026-08-19T00:00:00Z',
      },
    ],
  },
}));

vi.mock('@tanstack/react-query', () => ({
  useQuery: () => ({ data: queryMock.data, isLoading: false }),
  useMutation: () => ({ mutate: vi.fn(), isPending: false, error: null }),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));

vi.mock('@/components/common/auth-context', () => ({
  useAuth: () => ({ isAdmin: true }),
}));

const render = (): string => renderToStaticMarkup(createElement(UnpricedModelsTab));

describe('UnpricedModelsTab', () => {
  // #286: the raw id is what an admin copies into a rate table, so it is shown
  // as stored -- no display name, no alias.
  it('lists an unpriced model under its raw id with its token usage', () => {
    const markup = render();

    expect(markup).toContain('codex-auto-review');
    expect(markup).toContain('17,756,851');
    expect(markup).toContain('Mark flat-rate');
  });

  it('shows a blank model as missing rather than as an empty cell', () => {
    expect(render()).toContain('(empty model)');
  });

  it('lists the models marked flat-rate so the mark can be removed', () => {
    const markup = render();

    expect(markup).toContain('gemma4:12b');
    expect(markup).toContain('local ollama');
    expect(markup).toContain('Unmark');
  });
});
