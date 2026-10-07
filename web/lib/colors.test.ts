import { describe, expect, it } from 'vitest';
import { isAstra, matchAgentScope, modelColor, userColor, userSeriesColorMap } from './colors';

describe('modelColor', () => {
  it('assigns the established four shades to GPT-6 tiers', () => {
    expect([
      modelColor('gpt-6-astra'),
      modelColor('gpt-6-sol'),
      modelColor('gpt-6-terra'),
      modelColor('gpt-6-luna'),
    ]).toEqual([
      'var(--model-astra)',
      'oklch(0.55 0.135 232)',
      'oklch(0.65 0.122 232)',
      'oklch(0.77 0.099 232)',
    ]);
  });

  it('keeps the GPT-5.6 Sol, Terra, and Luna shades', () => {
    expect([
      modelColor('gpt-5.6-sol'),
      modelColor('gpt-5.6-terra'),
      modelColor('gpt-5.6-luna'),
    ]).toEqual([
      'oklch(0.55 0.135 232)',
      'oklch(0.65 0.122 232)',
      'oklch(0.77 0.099 232)',
    ]);
  });

  it('matches named tiers in later GPT versions and keeps the untiered fallback', () => {
    expect(modelColor('gpt-6.1-sol')).toBe('oklch(0.55 0.135 232)');
    expect(modelColor('gpt-6.1-terra')).toBe('oklch(0.65 0.122 232)');
    expect(modelColor('gpt-6.1-luna')).toBe('oklch(0.77 0.099 232)');
    expect(modelColor('gpt-6.1-astra')).toBe('var(--model-astra)');
    expect(modelColor('gpt-6.1')).toBe('oklch(0.80 0.105 232)');
  });

  it('recognizes versioned Astra consistently with its model color', () => {
    expect(isAstra('gpt-6.1-astra')).toBe(true);
    expect(isAstra('gpt-6.1-sol')).toBe(false);
  });
});

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
