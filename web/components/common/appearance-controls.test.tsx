import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { AppearanceControls } from './appearance-controls';

type AccentValue = 'indigo' | 'teal' | 'mono';

const appearanceMock = vi.hoisted(() => ({
  state: {
    accent: 'indigo' as AccentValue,
    agentTone: 'vivid',
    showLogoMark: true,
    setAccent: () => undefined,
    setAgentTone: () => undefined,
    setShowLogoMark: () => undefined,
  },
}));

vi.mock('./appearance-context', () => ({
  useAppearance: () => appearanceMock.state,
}));

const ACCENT_CASES: Array<{ value: AccentValue; label: string; swatch: string }> = [
  { value: 'indigo', label: 'Indigo', swatch: 'bg-accent-indigo' },
  { value: 'teal', label: 'Teal', swatch: 'bg-accent-teal' },
  { value: 'mono', label: 'Mono', swatch: 'bg-accent-mono' },
];

const render_controls = (accent: AccentValue): string => {
  appearanceMock.state.accent = accent;
  return renderToStaticMarkup(createElement(AppearanceControls));
};

const extract_buttons = (html: string): string[] =>
  [...html.matchAll(/<button\b[^>]*>[\s\S]*?<\/button>/g)].map(([markup]) => markup);

const find_button = (html: string, label: string): string => {
  const button = extract_buttons(html).find((markup) => markup.includes(label));
  return button ?? '';
};

const find_swatch = (html: string, swatch: string): string => {
  const pattern = new RegExp('<span\\b[^>]*class="[^"]*' + swatch + '[^"]*"[^>]*>\\s*</span>');
  return html.match(pattern)?.[0] ?? '';
};

const find_logo_switch = (html: string): string =>
  html.match(/<button\b[^>]*role="switch"[\s\S]*?<\/button>/)?.[0] ?? '';

const find_logo_thumb = (track: string): string =>
  track.match(/<span\b[^>]*class="[^"]*h-5[^"]*"[^>]*>\s*<\/span>/)?.[0] ?? '';

const TestAppearanceControls_RendersAccentButtonsWithPressedState = (): void => {
  const html = render_controls('indigo');

  const active = find_button(html, 'Indigo');
  const inactiveTeal = find_button(html, 'Teal');
  const inactiveMono = find_button(html, 'Mono');

  expect(active).toContain('aria-pressed="true"');
  expect(inactiveTeal).toContain('aria-pressed="false"');
  expect(inactiveMono).toContain('aria-pressed="false"');
  expect(active).toContain('bg-brand');
  expect(inactiveTeal).toContain('hover:bg-surface-sunk');
  expect(inactiveMono).toContain('hover:bg-surface-sunk');

  for (const option of ACCENT_CASES) {
    const swatch = find_swatch(html, option.swatch);
    expect(swatch).toContain('aria-hidden="true"');
    expect(swatch).toContain(option.swatch);
  }
};

const TestAppearanceControls_MarksExactlyOneAccentAsActive = (accent: AccentValue): void => {
  const html = render_controls(accent);
  const pressedValues = ACCENT_CASES.map((option) =>
    find_button(html, option.label).match(/aria-pressed="(true|false)"/)?.[1],
  );

  expect(pressedValues).toEqual(['indigo', 'teal', 'mono'].map((value) => String(value === accent)));
};

const TestAppearanceControls_UsesNeutralInnerMarkerForActiveSwatch = (): void => {
  const html = render_controls('teal');
  const activeSwatch = find_swatch(html, 'bg-accent-teal');
  const inactiveSwatches = [
    find_swatch(html, 'bg-accent-indigo'),
    find_swatch(html, 'bg-accent-mono'),
  ];

  expect(activeSwatch).toContain('ring-2');
  expect(activeSwatch).toContain('ring-surface');
  expect(activeSwatch).toContain('bg-accent-teal');
  for (const swatch of inactiveSwatches) {
    expect(swatch).not.toContain('ring-2');
    expect(swatch).not.toContain('ring-surface');
  }
};

const TestAppearanceControls_DoesNotUseInlineAccentColor = (): void => {
  const html = render_controls('mono');

  for (const option of ACCENT_CASES) {
    expect(find_button(html, option.label)).not.toContain('style=');
    expect(find_swatch(html, option.swatch)).not.toContain('style=');
  }
  expect(html).not.toMatch(/style="[^"]*(?:#5B5BD6|#0EA5A5|#2B313B)/);
};

describe('AppearanceControls accent selection', () => {
  it('ACC-P01 exposes pressed state and stable swatches', TestAppearanceControls_RendersAccentButtonsWithPressedState);

  it.each(ACCENT_CASES)('ACC-P02 marks exactly one active accent for $value', ({ value }) => {
    TestAppearanceControls_MarksExactlyOneAccentAsActive(value);
  });

  it('ACC-P03 adds a neutral marker without replacing the swatch color', TestAppearanceControls_UsesNeutralInnerMarkerForActiveSwatch);

  it('ACC-B03 keeps accent colors in classes rather than inline styles', TestAppearanceControls_DoesNotUseInlineAccentColor);
});

describe('AppearanceControls logo mark switch', () => {
  it.each([true, false])('LOGO-P01/P02 preserves accessible state and endpoint inset when %s', (showLogoMark) => {
    appearanceMock.state.showLogoMark = showLogoMark;
    const track = find_logo_switch(renderToStaticMarkup(createElement(AppearanceControls)));
    const thumb = find_logo_thumb(track);

    expect(track).toContain('role="switch"');
    expect(track).toContain('aria-label="Logo mark"');
    expect(track).toContain(`aria-checked="${showLogoMark}"`);
    expect(track).toContain('h-6 w-11');
    expect(track).toContain('border border-border');
    expect(track).not.toContain('overflow-hidden');
    expect(thumb).toContain('left-px');
    expect(thumb).toContain('top-px');
    expect(thumb).toContain('h-5 w-5');

    const trackWidth = 44;
    const trackHeight = 24;
    const borderWidth = 1;
    const thumbWidth = 20;
    const thumbHeight = 20;
    const anchor = 1;
    const outerInset = borderWidth + anchor;
    const travel = trackWidth - outerInset * 2 - thumbWidth;
    const transform = showLogoMark ? 20 : 0;

    expect(trackHeight - outerInset * 2 - thumbHeight).toBe(0);
    expect(travel).toBe(20);
    expect(thumb).toContain(showLogoMark ? 'translate-x-5' : 'translate-x-0');
    expect(transform).toBe(showLogoMark ? travel : 0);
    expect(trackWidth - (borderWidth + anchor + transform + thumbWidth)).toBe(showLogoMark ? outerInset : 22);
  });
});

describe('AppearanceControls retired density contract', () => {
  it('DEN-P01 removes the Density row and scopes the description to consumed agent markers', () => {
    const html = render_controls('mono');

    expect(html).not.toContain('Density');
    expect(html).not.toContain('Compact');
    expect(html).not.toContain('Regular');
    expect(html).not.toContain('Comfy');
    expect(html).toContain('Agent tone');
    expect(html).toContain('Tune accent and agent marker tone');
  });
});
