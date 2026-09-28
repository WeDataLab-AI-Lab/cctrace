import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { WINDOW_5H, WINDOW_7D, availableProvidersFor, availableWindowsFor, buildWindows } from '@/lib/quota-usage';
import type { SeriesKind } from '@/lib/quota-usage';
import type { QuotaSample } from '@/lib/types';
import { ToggleChip, UsageControls, UsageLegend } from './usage-chart';

const renderChip = (selected: boolean) => renderToStaticMarkup(createElement(ToggleChip, {
  selected,
  onClick: () => undefined,
  label: '5h',
}));

describe('ToggleChip selection state', () => {
  it('renders an included filter as pressed with the brand-selected treatment', () => {
    const html = renderChip(true);

    expect(html).toContain('aria-pressed="true"');
    expect(html).toContain('border-brand bg-brand text-brand-ink');
  });

  // The unpressed label has to be a foreground token. `text-muted` reads as one
  // but --muted is var(--surface-sunk) -- a background -- so it painted #F1F3F6
  // on #FFFFFF and the chip was legible only while selected.
  it('renders an excluded filter as unpressed with a readable label', () => {
    const html = renderChip(false);

    expect(html).toContain('aria-pressed="false"');
    expect(html).toContain('border-border bg-surface text-ink-2 hover:text-fg');
    expect(html).not.toContain('text-muted ');
    expect(html).not.toContain('bg-brand text-brand-ink');
  });
});

const quotaSample = (over: Partial<QuotaSample> & Pick<QuotaSample, 'account_id' | 'sampled_at' | 'used_pct'>): QuotaSample => ({
  billing_provider: 'anthropic',
  window_key: '300',
  window_minutes: WINDOW_5H,
  plan: 'default_claude_max_5x',
  ...over,
});

const columnMarkup = (html: string, label: string): string => {
  const match = html.match(new RegExp(`<div role="group" aria-label="${label}"[^>]*>(.*?)</div>`));
  expect(match, `${label} legend column`).not.toBeNull();
  return match?.[1] ?? '';
};

describe('Subscription burn filter discovery', () => {
  const renderControls = (
    samples: QuotaSample[],
    windows = availableWindowsFor(samples),
    providers = availableProvidersFor(samples),
    series: SeriesKind[] = ['accounts'],
  ) => {
    const availableWindows = availableWindowsFor(samples);
    const availableProviders = availableProvidersFor(samples);
    return renderToStaticMarkup(createElement(UsageControls, {
      availableWindows,
      availableProviders,
      windows,
      providers,
      onToggleWindow: () => () => undefined,
      onToggleProvider: () => () => undefined,
      series,
      onToggleSeries: () => () => undefined,
      soloWeightedAvailable: true,
    }));
  };

  const chipMarkup = (html: string, label: string): string => {
    const match = html.match(new RegExp(`<button[^>]*>\\s*${label}\\s*</button>`));
    expect(match, `${label} control`).not.toBeNull();
    return match?.[0] ?? '';
  };

  it('renders no 5h or Codex control for Spark-only OpenAI input', () => {
    const sparkOnly = [quotaSample({
      account_id: 'codex-spark',
      sampled_at: '2026-08-24T09:00:00Z',
      used_pct: 4,
      billing_provider: 'openai',
      plan: 'pro',
      window_key: 'codex_bengalfox:300',
      scope_label: 'GPT-5.3-Codex-Spark',
    })];

    const html = renderControls(sparkOnly);
    const windowSeries = buildWindows(sparkOnly, [WINDOW_5H], ['openai']);
    const legend = renderToStaticMarkup(createElement(UsageLegend, {
      windowSeries,
      colorIndex: new Map(),
    }));

    expect(html).not.toContain('5h');
    expect(html).not.toContain('Codex');
    expect(legend).not.toContain('weighted 5h');
    expect(legend).not.toContain('(Codex, 5h)');
    expect(windowSeries).toEqual([]);
  });

  it('preserves the 5h and Codex controls for a generic OpenAI meter', () => {
    const generic = [quotaSample({
      account_id: 'codex-general',
      sampled_at: '2026-08-24T09:00:00Z',
      used_pct: 40,
      billing_provider: 'openai',
      plan: 'pro',
      window_key: '300',
    })];

    const html = renderControls(generic);
    const windowSeries = buildWindows(generic, [WINDOW_5H], ['openai']);
    const legend = renderToStaticMarkup(createElement(UsageLegend, {
      windowSeries,
      colorIndex: new Map([['openai:codex-general', 0]]),
    }));

    expect(chipMarkup(html, '5h')).toContain('aria-pressed="true"');
    expect(chipMarkup(html, 'Codex')).toContain('aria-pressed="true"');
    expect(legend).toContain('weighted 5h');
    expect(legend).toContain('codex-general (Codex, 5h)');
    expect(windowSeries).toHaveLength(1);
  });

  // Selection is drawn as it is, including the pairs a click can now reach.
  // While a click could only isolate, "all" was drawn with every chip dark --
  // legible only because the sole alternative was a single lit chip. A control
  // that can hold a pair has to show which pair, so a selected chip reads
  // pressed whether one, some, or all of the axis is on.
  it('marks every selected window and provider as pressed', () => {
    const at = '2026-08-24T09:00:00Z';
    const samples = [
      quotaSample({ account_id: 'claude', sampled_at: at, used_pct: 10 }),
      quotaSample({ account_id: 'claude', sampled_at: at, used_pct: 20,
        window_key: '10080', window_minutes: WINDOW_7D }),
      quotaSample({ account_id: 'codex', sampled_at: at, used_pct: 30,
        billing_provider: 'openai', plan: 'pro' }),
      quotaSample({ account_id: 'codex', sampled_at: at, used_pct: 40,
        billing_provider: 'openai', plan: 'pro', window_key: '10080', window_minutes: WINDOW_7D }),
    ];

    const allHtml = renderControls(samples);
    for (const label of ['5h', '7d', 'Claude', 'Codex']) {
      expect(chipMarkup(allHtml, label)).toContain('aria-pressed="true"');
    }

    // A pair is a state of its own, distinct from all and from one.
    const pairHtml = renderControls(samples, [WINDOW_5H, WINDOW_7D], ['openai']);
    expect(chipMarkup(pairHtml, '5h')).toContain('aria-pressed="true"');
    expect(chipMarkup(pairHtml, '7d')).toContain('aria-pressed="true"');
    expect(chipMarkup(pairHtml, 'Claude')).toContain('aria-pressed="false"');
    expect(chipMarkup(pairHtml, 'Codex')).toContain('aria-pressed="true"');

    expect(allHtml).toContain('role="group" aria-label="Quota window"');
    expect(allHtml).toContain('role="group" aria-label="Billing provider"');

    const focusedHtml = renderControls(samples, [WINDOW_7D], ['openai']);
    expect(chipMarkup(focusedHtml, '5h')).toContain('aria-pressed="false"');
    expect(chipMarkup(focusedHtml, '7d')).toContain('aria-pressed="true"');
    expect(chipMarkup(focusedHtml, 'Claude')).toContain('aria-pressed="false"');
    expect(chipMarkup(focusedHtml, 'Codex')).toContain('aria-pressed="true"');

    const otherFocusHtml = renderControls(samples, [WINDOW_5H], ['anthropic']);
    expect(chipMarkup(otherFocusHtml, '5h')).toContain('aria-pressed="true"');
    expect(chipMarkup(otherFocusHtml, '7d')).toContain('aria-pressed="false"');
    expect(chipMarkup(otherFocusHtml, 'Claude')).toContain('aria-pressed="true"');
    expect(chipMarkup(otherFocusHtml, 'Codex')).toContain('aria-pressed="false"');

    const shown = buildWindows(samples, [WINDOW_7D], ['openai']);
    expect(shown.map((window) => window.windowMinutes)).toEqual([WINDOW_7D]);
    expect(shown.flatMap((window) => window.accounts).map((account) => account.provider)).toEqual(['openai']);
  });
});

describe('UsageLegend provider grouping', () => {
  it('keeps two accounts in one provider section alongside a separate provider-weighted line', () => {
    const at = '2026-08-24T09:00:00Z';
    const samples = [
      quotaSample({ account_id: 'alice', login_email: 'alice@example.test', sampled_at: at, used_pct: 20,
        billing_provider: 'openai', plan: 'pro' }),
      quotaSample({ account_id: 'bob', login_email: 'bob@example.test', sampled_at: at, used_pct: 80,
        billing_provider: 'openai', plan: 'pro' }),
    ];
    const windowSeries = buildWindows(samples, [WINDOW_5H], ['openai']);
    const html = renderToStaticMarkup(createElement(UsageLegend, {
      windowSeries,
      colorIndex: new Map([['openai:alice', 0], ['openai:bob', 1]]),
    }));
    const codex = columnMarkup(html, 'Codex');

    expect(codex).toContain('alice@example.test (Codex, 5h)');
    expect(codex).toContain('bob@example.test (Codex, 5h)');
  });

  it('renders weighted, Claude, and Codex series in separate responsive text columns', () => {
    const at = '2026-08-24T09:00:00Z';
    const samples = [
      quotaSample({ account_id: 'claude-id', login_email: 'claude@example.test', sampled_at: at, used_pct: 20 }),
      quotaSample({ account_id: 'claude-id', login_email: 'claude@example.test', sampled_at: at, used_pct: 40,
        window_key: '10080', window_minutes: WINDOW_7D }),
      quotaSample({ account_id: 'codex-id', sampled_at: at, used_pct: 60, billing_provider: 'openai', plan: 'pro' }),
      quotaSample({ account_id: 'codex-id', sampled_at: at, used_pct: 80, billing_provider: 'openai', plan: 'pro',
        window_key: '10080', window_minutes: WINDOW_7D }),
      quotaSample({ account_id: 'codex-id', sampled_at: at, used_pct: 99, billing_provider: 'openai', plan: 'pro',
        window_key: 'codex_bengalfox:300', scope_label: 'GPT-5.3-Codex-Spark' }),
    ];
    const windowSeries = buildWindows(samples, [WINDOW_5H, WINDOW_7D], ['anthropic', 'openai']);
    const html = renderToStaticMarkup(createElement(UsageLegend, {
      windowSeries,
      colorIndex: new Map([['anthropic:claude-id', 0], ['openai:codex-id', 1]]),
    }));

    expect(html).toContain('<section aria-label="Subscription burn legend"');
    expect(html).toContain('mx-auto w-fit max-w-full grid grid-cols-1 gap-y-2 sm:grid-cols-3');
    expect(html).not.toMatch(/class="[^"]*\b(?:rounded|border|bg-|px-|py-)/);
    expect(html).not.toContain('<h3');

    const weighted = columnMarkup(html, 'Weighted');
    expect(weighted).toContain('weighted 5h');
    expect(weighted).toContain('weighted 7d');
    expect(weighted).not.toContain('example.test');

    const claude = columnMarkup(html, 'Claude');
    expect(claude).not.toContain('weighted');
    expect(claude).toContain('claude@example.test (Claude, 5h)');
    expect(claude).toContain('claude@example.test (Claude, 7d)');
    expect(claude).not.toContain('(Codex,');

    const codex = columnMarkup(html, 'Codex');
    expect(codex).not.toContain('weighted');
    expect(codex).toContain('codex-id (Codex, 5h)');
    expect(codex).toContain('codex-id (Codex, 7d)');
    expect(codex).not.toContain('(Claude,');
    expect(html).not.toContain('Spark');
  });
});

describe('overall weighted visibility', () => {
  const at = '2026-08-24T09:00:00Z';
  const samples = [
    quotaSample({ account_id: 'claude-id', login_email: 'claude@example.test', sampled_at: at, used_pct: 20 }),
    quotaSample({ account_id: 'codex-id', login_email: 'codex@example.test', sampled_at: at, used_pct: 40,
      billing_provider: 'openai', plan: 'pro' }),
  ];
  const windowSeries = buildWindows(samples, [WINDOW_5H], ['anthropic', 'openai']);
  const colorIndex = new Map([['anthropic:claude-id', 0], ['openai:codex-id', 1]]);

  const renderLegend = (showWeighted: boolean) => renderToStaticMarkup(createElement(UsageLegend, {
    windowSeries,
    colorIndex,
    showWeighted,
  }));

  // The overall weighted line averages across providers, and a reader who wants
  // to know how one account is doing has to look past it to find them. It is off
  // until asked for, and its legend column goes with it -- a key for a line that
  // is not drawn sends the reader hunting for it.
  it('omits the weighted column and its keys when the line is hidden', () => {
    const html = renderLegend(false);

    expect(html).not.toContain('>weighted 5h<');
    expect(html).not.toContain('aria-label="Weighted"');
  });

  it('shows the weighted column when the line is drawn', () => {
    const html = renderLegend(true);

    expect(html).toContain('>weighted 5h<');
    expect(html).toContain('aria-label="Weighted"');
  });

  // A harness column lists that harness's accounts and nothing else. The
  // per-harness weighted roll-up that used to head each column is gone: the
  // accounts under it are already grouped by harness, so the extra line stated
  // the grouping a second time and put a stroke in front of the ones the reader
  // opened the chart for.
  it('lists only account keys under each harness column', () => {
    for (const html of [renderLegend(false), renderLegend(true)]) {
      expect(html).not.toContain('Claude weighted');
      expect(html).not.toContain('Codex weighted');
      expect(html).toContain('claude@example.test (Claude, 5h)');
      expect(html).toContain('codex@example.test (Codex, 5h)');
    }
  });
});

// The weighted control sits apart from the two filter axes because it is not a
// filter: the chips above narrow which readings are drawn, this one chooses
// whether an extra derived series joins them.
describe('weighted control', () => {
  const at = '2026-08-24T09:00:00Z';
  const samples = [
    quotaSample({ account_id: 'claude-id', sampled_at: at, used_pct: 20 }),
  ];

  const renderSeriesControls = (series: SeriesKind[], soloWeightedAvailable = true) =>
    renderToStaticMarkup(createElement(UsageControls, {
      availableWindows: availableWindowsFor(samples),
      availableProviders: availableProvidersFor(samples),
      windows: availableWindowsFor(samples),
      providers: availableProvidersFor(samples),
      onToggleWindow: () => () => undefined,
      onToggleProvider: () => () => undefined,
      series,
      onToggleSeries: () => () => undefined,
      soloWeightedAvailable,
    }));

  const chip = (html: string, label: string) => {
    const match = html.match(new RegExp(`<button[^>]*>\\s*${label}\\s*</button>`));
    expect(match, `${label} control`).not.toBeNull();
    return match?.[0] ?? '';
  };

  it('draws accounts by default with the weighted line off', () => {
    const html = renderSeriesControls(['accounts']);
    expect(html).toContain('role="group" aria-label="Series"');
    expect(chip(html, 'Accounts')).toContain('aria-pressed="true"');
    expect(chip(html, 'Weighted')).toContain('aria-pressed="false"');
  });

  // Each series is its own chip, so weighted-alone is one click and the state is
  // legible from the chips rather than from how many times they were pressed.
  it('shows weighted alone as its own pressed state', () => {
    const html = renderSeriesControls(['weighted']);
    expect(chip(html, 'Accounts')).toContain('aria-pressed="false"');
    expect(chip(html, 'Weighted')).toContain('aria-pressed="true"');
  });

  // A price-weighted mean over one priced account IS that account's line, drawn
  // heavier on top of itself. Offering the choice invites reading corroboration
  // into a duplicate.
  it('hides the series group when only one priced account is drawn', () => {
    const html = renderSeriesControls(['accounts'], false);
    expect(html).not.toContain('aria-label="Series"');
  });
});

// The legend swatch has to match the stroke it stands for, and both now read the
// count of windows actually drawn -- not the count selected. A window whose
// range holds no readings is dropped from the series, so selecting two and
// drawing one would otherwise dash the chart while the key stayed solid.
describe('legend swatches follow the drawn window count', () => {
  const at = '2026-08-24T09:00:00Z';
  const samples = [
    quotaSample({ account_id: 'claude-id', login_email: 'claude@example.test', sampled_at: at, used_pct: 20 }),
    quotaSample({ account_id: 'claude-id', login_email: 'claude@example.test', sampled_at: at, used_pct: 30,
      window_key: '10080', window_minutes: WINDOW_7D }),
  ];

  const legendFor = (windows: number[]) => renderToStaticMarkup(createElement(UsageLegend, {
    windowSeries: buildWindows(samples, windows, ['anthropic']),
    colorIndex: new Map([['anthropic:claude-id', 0]]),
  }));

  it('draws solid keys when one window is on screen', () => {
    expect(legendFor([WINDOW_5H])).not.toContain('stroke-dasharray');
    expect(legendFor([WINDOW_7D])).not.toContain('stroke-dasharray');
  });

  it('dashes only the weekly key when both are on screen', () => {
    const html = legendFor([WINDOW_5H, WINDOW_7D]);
    const keys = html.split('<li').filter((chunk) => chunk.includes('5h)') || chunk.includes('7d)'));

    expect(keys.find((k) => k.includes('5h)'))).not.toContain('stroke-dasharray');
    expect(keys.find((k) => k.includes('7d)'))).toContain('stroke-dasharray');
  });
});
