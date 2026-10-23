import { renderToStaticMarkup } from 'react-dom/server';
import { expect, it } from 'vitest';
import { SummaryCard } from './summary-card';
import { ToolsSummaryCards } from '@/components/tools/tools-summary-cards';
import { UsageSummary } from '@/components/plugins/usage-summary';

it('keeps generic card descriptions inline unless compact presentation is requested', () => {
  const markup = renderToStaticMarkup(<SummaryCard title="Metric" value={42} subtitle="This month" note="Estimate" />);
  for (const text of ['Metric', '42', 'This month', 'Estimate']) expect(markup).toContain(text);
  expect(markup).not.toContain('<button');
});

it('preserves the tools summary labels and inline time range', () => {
  const markup = renderToStaticMarkup(<ToolsSummaryCards totalUse={100} totalSuccess={80} totalFail={20} overallRate="80" />);
  for (const text of ['Total Uses', 'This month', 'Success', 'Failures', 'Success Rate', '80%']) expect(markup).toContain(text);
  expect(markup).not.toContain('<button');
});

it('preserves plugin usage summaries without adding explanation controls', () => {
  const markup = renderToStaticMarkup(<UsageSummary pluginCalls={10} skillCalls={20} calls={30} totalTokens={300} inputTokens={100} outputTokens={200} />);
  for (const text of ['Claude Calls', 'Codex Calls', 'Avg Tokens', 'Total', 'Input', 'Output']) expect(markup).toContain(text);
  expect(markup).not.toContain('<button');
});

// Loading used to print the sum of an empty list -- a 0 that looked like a real count.
// It now holds a moving placeholder in the number's place until the data is in.
it('holds a placeholder instead of a number while loading', () => {
  const markup = renderToStaticMarkup(<SummaryCard title="Metric" value={0} loading />);
  expect(markup).toContain('Metric');
  expect(markup).toContain('cctrace-shimmer');
  expect(markup).toContain('aria-busy="true"');
  expect(markup).not.toMatch(/>0</);
});

it('keeps zeros and partial sums off the tools and plugin cards while loading', () => {
  const tools = renderToStaticMarkup(<ToolsSummaryCards totalUse={0} totalSuccess={0} totalFail={0} overallRate="-" loading />);
  expect(tools.match(/cctrace-shimmer/g)).toHaveLength(4);
  expect(tools).not.toMatch(/>0</);
  // Claude rows arrive before Codex rows; 260 alone is not the total yet.
  const plugins = renderToStaticMarkup(<UsageSummary pluginCalls={260} skillCalls={0} calls={260} totalTokens={58_000_000} inputTokens={49_500_000} outputTokens={8_400_000} loading />);
  expect(plugins.match(/cctrace-shimmer/g)).toHaveLength(6);
  expect(plugins).not.toContain('260');
  expect(plugins).not.toContain('58.0M');
});
