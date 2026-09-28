import { describe, expect, it } from 'vitest';
import { WEEKLY_USAGE_HINT, isWeeklyUsageKey } from './weekly-usage';

describe('isWeeklyUsageKey', () => {
  it('recognises the key the server gives weekly report usage', () => {
    expect(isWeeklyUsageKey('weekly')).toBe(true);
    expect(isWeeklyUsageKey('')).toBe(false);
    expect(isWeeklyUsageKey(undefined)).toBe(false);
  });

  it('explains the line in Korean', () => {
    expect(WEEKLY_USAGE_HINT).toContain('주간 AI 리포트');
  });
});
