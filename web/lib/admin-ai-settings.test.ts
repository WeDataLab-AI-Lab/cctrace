import { describe, expect, it } from 'vitest';

import {
  EFFORT_INHERIT,
  catalogErrorMessage,
  effortChoices,
  effortForModel,
  modelChoices,
  settingsDraftView,
} from './admin-ai-settings';
import type { AdminAIModel, AdminAISettings } from './types';

const models: AdminAIModel[] = [
  {
    id: 'gpt-5.6-terra',
    display_name: 'GPT-5.6-Terra',
    description: '',
    is_default: false,
    default_reasoning_effort: 'medium',
    supported_reasoning_efforts: [
      { reasoning_effort: 'low', description: '' },
      { reasoning_effort: 'medium', description: '' },
      { reasoning_effort: 'high', description: '' },
    ],
  },
  {
    id: 'gpt-5.5',
    display_name: 'GPT-5.5',
    description: '',
    is_default: false,
    default_reasoning_effort: 'low',
    supported_reasoning_efforts: [{ reasoning_effort: 'low', description: '' }],
  },
];

// Model from the default, effort from the environment: nothing an admin chose.
const settings: AdminAISettings = {
  runtime: 'codex-app-server',
  model: 'gpt-5.6-terra',
  reasoning_effort: 'high',
  base_url: '',
  source: { model: 'default', reasoning_effort: 'env', base_url: 'default' },
  env_model: '',
  env_reasoning_effort: 'high',
  env_base_url: '',
};

describe('modelChoices', () => {
  it('labels catalog models with display name and id', () => {
    expect(modelChoices(models, 'gpt-5.5')).toEqual([
      { value: 'gpt-5.6-terra', label: 'GPT-5.6-Terra · gpt-5.6-terra' },
      { value: 'gpt-5.5', label: 'GPT-5.5 · gpt-5.5' },
    ]);
  });

  it('keeps a current model the catalog no longer lists, marked', () => {
    const choices = modelChoices(models, 'gpt-old');
    expect(choices).toHaveLength(3);
    expect(choices[2]).toEqual({ value: 'gpt-old', label: 'gpt-old (목록에 없음)' });
  });
});

describe('effortChoices', () => {
  it('limits efforts to the model and names the model default when nothing is inherited', () => {
    expect(effortChoices(models, 'gpt-5.5', '', EFFORT_INHERIT)).toEqual([
      { value: EFFORT_INHERIT, label: '모델 기본값 (low)' },
      { value: 'low', label: 'low' },
    ]);
  });

  it('names the environment value an empty effort inherits', () => {
    expect(effortChoices(models, 'gpt-5.6-terra', 'high', EFFORT_INHERIT)[0]).toEqual({
      value: EFFORT_INHERIT,
      label: '환경변수 값 (high)',
    });
  });

  it('drops the inherit choice when the model cannot take the environment value', () => {
    expect(effortChoices(models, 'gpt-5.5', 'high', 'low').map((c) => c.value)).toEqual(['low']);
  });

  it('keeps a current value the model does not support, marked, so the select is never blank', () => {
    expect(effortChoices(models, 'gpt-5.5', '', 'high')).toContainEqual({ value: 'high', label: 'high (지원 안 됨)' });
  });
});

describe('effortForModel', () => {
  it('keeps an effort the new model supports', () => {
    expect(effortForModel(models, 'gpt-5.6-terra', '', 'medium')).toBe('medium');
  });

  it('inherits when the new model lacks the effort and nothing unsupported would be inherited', () => {
    expect(effortForModel(models, 'gpt-5.5', '', 'high')).toBe(EFFORT_INHERIT);
  });

  it('picks the model default explicitly when the inherited environment value would not fit', () => {
    expect(effortForModel(models, 'gpt-5.5', 'high', EFFORT_INHERIT)).toBe('low');
  });
});

describe('settingsDraftView base_url', () => {
  // The address is the one provider setting an admin types rather than picks,
  // and it follows the same rule as the model: only a value the admin actually
  // set is sent back, so an untouched one keeps following the environment
  // instead of being frozen as an admin choice.
  it('sends nothing while the address still follows the environment', () => {
    const envOnly = {
      ...settings,
      base_url: 'https://env.example.test',
      env_base_url: 'https://env.example.test',
      source: { model: 'default' as const, reasoning_effort: 'env' as const, base_url: 'env' as const },
    };
    const view = settingsDraftView(envOnly, null);
    expect(view.baseUrl).toBe('https://env.example.test');
    expect(view.request.base_url).toBe('');
    expect(view.dirty).toBe(false);
  });

  it('sends the address once the admin types one', () => {
    const envOnly = {
      ...settings,
      base_url: 'https://env.example.test',
      env_base_url: 'https://env.example.test',
      source: { model: 'default' as const, reasoning_effort: 'env' as const, base_url: 'env' as const },
    };
    const view = settingsDraftView(envOnly, { model: envOnly.model, effort: 'inherit', baseUrl: 'https://typed.example.test' });
    expect(view.request.base_url).toBe('https://typed.example.test');
    expect(view.dirty).toBe(true);
  });

  it('keeps an address the admin already saved', () => {
    const admin = {
      ...settings,
      base_url: 'https://saved.example.test',
      source: { model: 'default' as const, reasoning_effort: 'env' as const, base_url: 'admin' as const },
    };
    const view = settingsDraftView(admin, null);
    expect(view.baseUrl).toBe('https://saved.example.test');
    expect(view.request.base_url).toBe('https://saved.example.test');
    expect(view.dirty).toBe(false);
  });

  it('clears a saved address when the admin empties the field', () => {
    const admin = {
      ...settings,
      base_url: 'https://saved.example.test',
      source: { model: 'default' as const, reasoning_effort: 'env' as const, base_url: 'admin' as const },
    };
    const view = settingsDraftView(admin, { model: admin.model, effort: 'inherit', baseUrl: '' });
    expect(view.request.base_url).toBe('');
    expect(view.dirty).toBe(true);
  });
});

describe('settingsDraftView', () => {
  it('shows the effective model and inherits a non-admin effort until the admin edits', () => {
    expect(settingsDraftView(settings, null)).toMatchObject({ model: 'gpt-5.6-terra', effort: EFFORT_INHERIT, dirty: false });
  });

  it('shows an admin effort as the chosen value', () => {
    const admin = { ...settings, source: { model: 'default' as const, reasoning_effort: 'admin' as const, base_url: 'default' as const } };
    expect(settingsDraftView(admin, null).effort).toBe('high');
  });

  it('sends empty values for items that did not come from the admin and were not changed', () => {
    const view = settingsDraftView(settings, { model: 'gpt-5.6-terra', effort: 'low', baseUrl: settings.base_url });
    expect(view.dirty).toBe(true);
    expect(view.request).toEqual({ model: '', reasoning_effort: 'low', base_url: '' });
  });

  it('keeps an existing admin model when only the effort changes', () => {
    const admin = { ...settings, model: 'gpt-5.5', source: { model: 'admin' as const, reasoning_effort: 'env' as const, base_url: 'default' as const } };
    expect(settingsDraftView(admin, { model: 'gpt-5.5', effort: 'low', baseUrl: admin.base_url }).request).toEqual({ model: 'gpt-5.5', reasoning_effort: 'low', base_url: '' });
  });

  it('sends a changed model as an admin choice', () => {
    const view = settingsDraftView(settings, { model: 'gpt-5.5', effort: EFFORT_INHERIT, baseUrl: settings.base_url });
    expect(view.request).toEqual({ model: 'gpt-5.5', reasoning_effort: '', base_url: '' });
  });

  it('is not dirty when the draft matches the saved override', () => {
    expect(settingsDraftView(settings, { model: 'gpt-5.6-terra', effort: EFFORT_INHERIT, baseUrl: settings.base_url }).dirty).toBe(false);
  });
});

describe('catalogErrorMessage', () => {
  it('tells each catalog failure apart', () => {
    expect(catalogErrorMessage('runtime_unconfigured')).toContain('설정되지');
    expect(catalogErrorMessage('runtime_not_logged_in')).toContain('로그인');
    expect(catalogErrorMessage('catalog_unavailable')).toContain('불러오지 못했습니다');
    expect(catalogErrorMessage('http_500')).toContain('불러오지 못했습니다');
  });
});
