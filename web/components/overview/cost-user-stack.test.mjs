import { describe, expect, it } from 'vitest';
import { buildCostModelData, buildCostUserStackData, costUserStackKey } from './cost-user-stack.ts';

describe('cost user stack data', () => {
  it('keeps sonnet as its own versioned model stack even when metadata is misleading', () => {
    expect(costUserStackKey('claude-sonnet-4-6', 'codex', 'openai')).toBe('sonnet 4.6');
  });

  it('keeps Claude 3.x versions when the version appears before the tier', () => {
    expect(costUserStackKey('claude-3-5-sonnet-20241022', 'claude', 'anthropic')).toBe('sonnet 3.5');
    expect(costUserStackKey('claude-3-7-sonnet-20250219', 'claude', 'anthropic')).toBe('sonnet 3.7');

    const result = buildCostModelData({
      viewMode: 'cost',
      rows: [
        {
          model: 'claude-3-5-sonnet-20241022',
          agent: 'claude',
          billing_provider: 'anthropic',
          total_cost: 10,
          input_tokens: 100,
          output_tokens: 100,
        },
        {
          model: 'claude-3-7-sonnet-20250219',
          agent: 'claude',
          billing_provider: 'anthropic',
          total_cost: 20,
          input_tokens: 200,
          output_tokens: 200,
        },
      ],
    });

    expect(result.data).toEqual([
      { name: 'sonnet 3.7', value: 20 },
      { name: 'sonnet 3.5', value: 10 },
    ]);
  });

  it('returns Claude models first, then Codex, then Compatible regardless of values', () => {
    const result = buildCostUserStackData({
      viewMode: 'cost',
      nameMap: {},
      rows: [
        {
          user_id: 'user-a',
          model: 'qwen-3',
          agent: 'claude',
          billing_provider: 'openrouter',
          total_cost: 30,
          total_input_tokens: 300,
          total_output_tokens: 300,
        },
        {
          user_id: 'user-a',
          model: 'claude-haiku-4-5',
          agent: 'claude',
          billing_provider: 'anthropic',
          total_cost: 25,
          total_input_tokens: 250,
          total_output_tokens: 250,
        },
        {
          user_id: 'user-a',
          model: 'gpt-5.5',
          agent: 'codex',
          billing_provider: 'openai',
          total_cost: 20,
          total_input_tokens: 200,
          total_output_tokens: 200,
        },
        {
          user_id: 'user-a',
          model: 'gpt-5.6-luna',
          agent: 'codex',
          billing_provider: 'openai',
          total_cost: 19,
          total_input_tokens: 190,
          total_output_tokens: 190,
        },
        {
          user_id: 'user-a',
          model: 'gpt-5.6-terra',
          agent: 'codex',
          billing_provider: 'openai',
          total_cost: 18,
          total_input_tokens: 180,
          total_output_tokens: 180,
        },
        {
          user_id: 'user-a',
          model: 'gpt-5.6-sol',
          agent: 'codex',
          billing_provider: 'openai',
          total_cost: 17,
          total_input_tokens: 170,
          total_output_tokens: 170,
        },
        {
          user_id: 'user-a',
          model: 'claude-sonnet-4-6',
          agent: 'claude',
          billing_provider: 'anthropic',
          total_cost: 10,
          total_input_tokens: 100,
          total_output_tokens: 100,
        },
        {
          user_id: 'user-a',
          model: 'claude-opus-4-8',
          agent: 'claude',
          billing_provider: 'anthropic',
          total_cost: 5,
          total_input_tokens: 50,
          total_output_tokens: 50,
        },
      ],
    });

    expect(result.stackKeys).toEqual(['opus 4.8', 'sonnet 4.6', 'haiku 4.5', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.5', 'qwen-3']);
    expect(result.data).toEqual([
      {
        name: 'user-a',
        'opus 4.8': 5,
        'sonnet 4.6': 10,
        'haiku 4.5': 25,
        'gpt-5.6-sol': 17,
        'gpt-5.6-terra': 18,
        'gpt-5.6-luna': 19,
        'gpt-5.5': 20,
        'qwen-3': 30,
      },
    ]);
  });

  it('orders GPT-6 tiers Astra, Sol, Terra, Luna in the user stack legend', () => {
    const result = buildCostUserStackData({
      viewMode: 'cost',
      nameMap: {},
      rows: ['gpt-6-luna', 'gpt-6-terra', 'gpt-6-sol', 'gpt-6-astra'].map((model) => ({
        user_id: 'user-a',
        model,
        agent: 'codex',
        billing_provider: 'openai',
        total_cost: 1,
        total_input_tokens: 10,
        total_output_tokens: 10,
      })),
    });

    expect(result.stackKeys).toEqual(['gpt-6-astra', 'gpt-6-sol', 'gpt-6-terra', 'gpt-6-luna']);
  });

  it('keeps compatible models as their own slice instead of Others', () => {
    const result = buildCostModelData({
      viewMode: 'cost',
      rows: [
        {
          model: 'qwen-3',
          agent: 'claude',
          billing_provider: 'openrouter',
          total_cost: 30,
          input_tokens: 300,
          output_tokens: 300,
        },
        {
          model: 'gpt-5.6-sol',
          agent: 'codex',
          billing_provider: 'openai',
          total_cost: 5,
          input_tokens: 50,
          output_tokens: 50,
        },
        {
          model: 'gpt-5.6-terra',
          agent: 'codex',
          billing_provider: 'openai',
          total_cost: 4,
          input_tokens: 40,
          output_tokens: 40,
        },
        {
          model: 'gpt-5.6-luna',
          agent: 'codex',
          billing_provider: 'openai',
          total_cost: 3,
          input_tokens: 30,
          output_tokens: 30,
        },
        {
          model: 'gpt-5.5',
          agent: 'codex',
          billing_provider: 'openai',
          total_cost: 20,
          input_tokens: 200,
          output_tokens: 200,
        },
        {
          model: 'claude-sonnet-4-6',
          agent: 'claude',
          billing_provider: 'anthropic',
          total_cost: 10,
          input_tokens: 100,
          output_tokens: 100,
        },
      ],
    });

    expect(result.data).toEqual([
      { name: 'sonnet 4.6', value: 10 },
      { name: 'gpt-5.6-sol', value: 5 },
      { name: 'gpt-5.6-terra', value: 4 },
      { name: 'gpt-5.6-luna', value: 3 },
      { name: 'gpt-5.5', value: 20 },
      { name: 'qwen-3', value: 30 },
    ]);
    expect(result.otherNames).toEqual([]);
  });

  // Usage that reached the store without a user_id is still spent money: it is a
  // bar of its own under a readable label, counted in the totals, and never folded
  // into a real user whose id happens to read "unknown".
  it('shows empty and missing user_id usage as one labeled Unknown user bar', () => {
    const row = (user_id, total_cost) => ({
      ...(user_id === undefined ? {} : { user_id }),
      model: 'claude-sonnet-4-6',
      agent: 'claude',
      billing_provider: 'anthropic',
      total_cost,
      total_input_tokens: total_cost * 10,
      total_output_tokens: total_cost * 10,
    });

    const result = buildCostUserStackData({
      viewMode: 'cost',
      nameMap: {},
      rows: [row('', 3), row(undefined, 4), row('unknown', 5), row('user-a', 1)],
    });

    expect(result.data).toEqual([
      { name: 'Unknown user', 'sonnet 4.6': 7 },
      { name: 'unknown', 'sonnet 4.6': 5 },
      { name: 'user-a', 'sonnet 4.6': 1 },
    ]);
    expect(result.stackKeys).toEqual(['sonnet 4.6']);
  });
});
