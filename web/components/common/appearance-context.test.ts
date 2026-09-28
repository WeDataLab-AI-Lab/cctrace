import { readFileSync } from 'node:fs';
import { describe, expect, it, vi } from 'vitest';
import { modelColor } from '@/lib/colors';
import {
  applyToDom,
  normalizeAppearancePayload,
  parseAppearancePayload,
  serializeAppearancePayload,
} from './appearance-context';

const DEFAULT_APPEARANCE = {
  accent: 'mono',
  agentTone: 'vivid',
  showLogoMark: true,
};

const GLOBALS_CSS = readFileSync(new URL('../../app/globals.css', import.meta.url), 'utf8');

describe('Appearance storage normalization', () => {
  it('STORE-P01 ignores legacy density and unknown fields in canonical output', () => {
    const normalized = normalizeAppearancePayload({
      density: 'comfy',
      accent: 'teal',
      agentTone: 'muted',
      showLogoMark: false,
      legacyOption: 'discard-me',
    });

    expect(normalized).toEqual({
      accent: 'teal',
      agentTone: 'muted',
      showLogoMark: false,
    });
    expect(serializeAppearancePayload(normalized)).toBe(
      '{"accent":"teal","agentTone":"muted","showLogoMark":false}',
    );
    expect(serializeAppearancePayload(normalized)).not.toContain('density');
  });

  it.each([null, '', '{', 'null', '[]', '0', '{"accent":"purple"}'])(
    'STORE-N01 falls back safely for malformed payload %s',
    (raw) => {
      expect(parseAppearancePayload(raw)).toEqual(DEFAULT_APPEARANCE);
    },
  );
});

describe('Appearance DOM contract', () => {
  it('DEN-B01 removes legacy data-density while preserving agent tone attributes', () => {
    const dataset: Record<string, string> = { density: 'comfy' };
    vi.stubGlobal('document', { documentElement: { dataset } });

    applyToDom('teal', 'muted');

    expect(dataset).toEqual({ accent: 'teal', agenttone: 'muted' });

    applyToDom('mono', 'vivid');

    expect(dataset).toEqual({});
    vi.unstubAllGlobals();
  });
});

describe('Appearance CSS regression contract', () => {
  it('TONE-N01 keeps agent-tone consumers and removes retired density CSS', () => {
    expect(GLOBALS_CSS).not.toMatch(/data-density|--row-h|--gap(?:\s|:)|--fs-base/);
    expect(GLOBALS_CSS).toContain(':root[data-agenttone="muted"]');
    expect(GLOBALS_CSS).toContain('--agent-claude');
    expect(GLOBALS_CSS).toContain('--model-fable');
    expect(GLOBALS_CSS).toContain('--model-astra');
  });
});

describe('General model palette contract', () => {
  it('TONE-N02 does not expand tone state into general model colors', () => {
    expect(modelColor('claude-opus-4-8')).toBe('oklch(0.66 0.127 39)');
    expect(modelColor('gpt-5.6-terra')).toBe('oklch(0.65 0.122 232)');
    expect(modelColor('qwen-3-coder')).toBe('oklch(0.63 0.095 182)');
    expect(modelColor('Others')).toBe('var(--agent-compat)');
  });

  // gpt-6-astra는 GPT-6 플래그십으로 sol의 자리를 넘겨받는다: astra가 토큰(가장
  // 짙음)을 갖고, sol은 자기 hue 램프 안 raw OKLCH로 내려간다(opus가 이미 그렇듯).
  it('gpt-6-astra가 새 플래그십 토큰을 갖고, sol은 raw OKLCH로 내려간다', () => {
    expect(modelColor('gpt-6-astra')).toBe('var(--model-astra)');
    expect(modelColor('gpt-5.6-sol')).toBe('oklch(0.55 0.135 232)');
  });
});
