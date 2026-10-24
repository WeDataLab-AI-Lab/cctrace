// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import RulesPage from './page';

vi.mock('@tanstack/react-query', () => ({
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useMutation: () => ({ mutate: vi.fn(), isPending: false }),
  useQuery: ({ queryKey }: { queryKey: string[] }) => queryKey[0] === 'project-rules'
    ? { data: { items: [] }, isLoading: false, isError: false }
    : { data: { rule: { current_version_id: 1 }, versions: [{ id: 1 }], comments: [] }, isLoading: false, isError: false },
}));
vi.mock('@/components/common/agent-context', () => ({ useAgent: () => ({ selectedAgent: '' }) }));
vi.mock('@/lib/rules-query', () => ({ useDebouncedValue: (value: string) => value }));
vi.mock('@/components/rules/rule-helpers', () => ({ buildRuleRows: () => [{ key: 'rule-1', rule: { id: 1, repository_key: 'repo', repository_name: 'Repo' } }] }));
vi.mock('@/components/common/summary-card', () => ({ SummaryCard: () => null }));
vi.mock('@/components/rules/rules-filters', () => ({ RulesFilters: () => null }));
vi.mock('@/components/rules/rule-list', () => ({ RuleList: ({ onSelectRule }: { onSelectRule: (key: string) => void }) => <button onClick={() => onSelectRule('rule-1')}>Open rule</button> }));
vi.mock('@/components/rules/rule-detail', () => ({ RuleDetail: () => <div>Rule detail</div> }));

afterEach(cleanup);

it('opens a rule as a single mobile detail and returns to the retained list selection', () => {
  const { container } = render(<RulesPage />);
  expect(container.firstElementChild?.getAttribute('data-mobile-view')).toBe('list');
  fireEvent.click(screen.getByRole('button', { name: 'Open rule' }));
  expect(container.firstElementChild?.getAttribute('data-mobile-view')).toBe('detail');
  expect(screen.getByText('Rule detail')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: /Back to rules/ }));
  expect(container.firstElementChild?.getAttribute('data-mobile-view')).toBe('list');
  expect(screen.getByRole('button', { name: 'Open rule' })).toBeTruthy();
});
