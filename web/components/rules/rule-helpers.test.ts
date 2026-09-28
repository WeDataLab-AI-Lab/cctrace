import { describe, expect, it } from 'vitest';
import { buildRuleRows } from './rule-helpers';
import type { ProjectRuleListItem } from '@/lib/types';

const rule: ProjectRuleListItem = {
  id: 7,
  agent: 'codex',
  repository_key: 'github.com/org/repo',
  rule_path: 'AGENTS.md',
  rule_kind: 'agents',
  rule_scope: 'repository',
  current_status: 'active',
  discovered_at: '2026-08-27T00:00:00Z',
  last_seen_at: '2026-08-27T00:00:00Z',
  updated_at: '2026-08-27T00:00:00Z',
  version_count: 1,
  comment_count: 0,
};

describe('buildRuleRows', () => {
  it('shows only rule snapshots returned by the API', () => {
    expect(buildRuleRows([])).toEqual([]);
    expect(buildRuleRows([rule])).toEqual([{ key: '7', rule }]);
  });
});
