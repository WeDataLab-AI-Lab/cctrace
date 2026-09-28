import { describe, expect, it } from 'vitest';
import { modelLabel, shortModel } from './model-label';
import { costUserStackKey } from '@/components/overview/cost-user-stack';

describe('modelLabel', () => {
  it('formats claude models as tier + dotted version', () => {
    expect(modelLabel('claude-sonnet-4-6')).toBe('sonnet 4.6');
    expect(modelLabel('claude-3-5-sonnet-20241022')).toBe('sonnet 3.5');
  });

  it('keeps compatible models as their own label instead of Others', () => {
    expect(modelLabel('qwen-3')).toBe('qwen-3');
  });

  it('keeps codex models as their own label', () => {
    expect(modelLabel('gpt-5.6-sol')).toBe('gpt-5.6-sol');
    expect(modelLabel('gpt-6-astra')).toBe('gpt-6-astra');
  });

  it('matches the cost-user-stack SSOT label exactly', () => {
    expect(modelLabel('claude-sonnet-4-6')).toBe(costUserStackKey('claude-sonnet-4-6'));
    expect(modelLabel('qwen-3')).toBe(costUserStackKey('qwen-3'));
    expect(modelLabel('gpt-5.6-sol')).toBe(costUserStackKey('gpt-5.6-sol'));
  });
});

describe('shortModel as filter value (#91 follow-up)', () => {
  // The server matches the filter value with raw `model LIKE '%<value>%'`
  // (postgres_stats_user.go / postgres_stats.go). modelLabel ('sonnet 4.6')
  // is NOT a substring of the raw id, so it must never be the filter value —
  // shortModel is. This regression guard fixes the P1 that broke Claude filters.
  const raws = ['claude-sonnet-4-6', 'claude-3-5-sonnet-20241022', 'qwen-3', 'gpt-5.6-sol'];

  it('filter value is a substring of the raw model id (server LIKE-matchable)', () => {
    for (const raw of raws) {
      expect(raw.includes(shortModel(raw))).toBe(true);
    }
    // The display label would break the server match — proving why value != label.
    expect('claude-sonnet-4-6'.includes(modelLabel('claude-sonnet-4-6'))).toBe(false);
  });

  it('renders the same pretty label whether given the raw id or the shortModel value', () => {
    for (const raw of raws) {
      expect(modelLabel(shortModel(raw))).toBe(modelLabel(raw));
    }
  });
});
