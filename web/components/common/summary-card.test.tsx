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
