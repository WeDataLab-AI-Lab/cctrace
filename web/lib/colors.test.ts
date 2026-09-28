import { describe, expect, it } from 'vitest';
import { matchAgentScope, userColor, userSeriesColorMap } from './colors';

describe('matchAgentScope', () => {
  it('keeps weekly report usage out of the other scope', () => {
    expect(matchAgentScope('weekly', 'other')).toBe(false);
    expect(matchAgentScope('gjc', 'other')).toBe(true);
    expect(matchAgentScope('omo', 'other')).toBe(true);
  });

  it('selects weekly usage under its own scope and under All', () => {
    expect(matchAgentScope('weekly', 'weekly')).toBe(true);
    expect(matchAgentScope('codex', 'weekly')).toBe(false);
    expect(matchAgentScope('weekly', '')).toBe(true);
  });
});

describe('userSeriesColorMap', () => {
  it('paints the weekly line with its own token, not a person slot', () => {
    expect(userSeriesColorMap(['weekly'])).toEqual({ weekly: 'var(--agent-weekly)' });
  });

  it('gives people the palette in order, skipping the weekly line', () => {
    expect(userSeriesColorMap(['3f9a0c', 'weekly', 'b71e02'])).toEqual({
      '3f9a0c': userColor(0),
      weekly: 'var(--agent-weekly)',
      b71e02: userColor(1),
    });
  });
});
