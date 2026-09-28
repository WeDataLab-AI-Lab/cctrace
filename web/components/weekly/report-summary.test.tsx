import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ReportSummary } from './report-summary';

describe('ReportSummary', () => {
  // Rule output and model output share the page; the tag says which is which (#672).
  it('always tags the summary as written by AI', () => {
    const html = renderToStaticMarkup(createElement(ReportSummary, { summary: '이번 주는 구간 21개가 몰렸다.' }));

    expect(html).toContain('요약');
    expect(html).toContain('AI 작성');
    expect(html).toContain('이번 주는 구간 21개가 몰렸다.');
  });

  it('heads the block at section size', () => {
    expect(renderToStaticMarkup(createElement(ReportSummary, { summary: 's' }))).toMatch(/<h3/);
  });
});
