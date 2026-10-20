import { createElement } from 'react';
import { cleanup, render } from '@testing-library/react';
import { afterEach, beforeAll, vi } from 'vitest';
import { PREVIEW_SESSIONS, analysisFor } from '@/app/test/session-analysis/fixtures';
import { DETAIL_NOW } from '@/app/test/session-analysis/detail/builders';
import { flagship } from '@/app/test/session-analysis/detail/flagship';
import type { SessionAnalysisFull } from '@/lib/session-analysis-types';
import { SessionAnalysisTab } from './analysis-tab';
import { isFullAnalysis } from './guards';

beforeAll(() => {
  // jsdom에 없는 브라우저 기능.
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  Element.prototype.scrollIntoView = vi.fn();
  window.matchMedia = (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  });
});

afterEach(cleanup);

const flagshipFull = (): SessionAnalysisFull => {
  const a = flagship().analysis;
  if (!a || !isFullAnalysis(a)) throw new Error('flagship must be a full analysis');
  return a;
};

const passA = (id: string): SessionAnalysisFull => {
  const fixture = PREVIEW_SESSIONS.find((f) => f.id === id);
  const analysis = fixture ? analysisFor(fixture, 'owner') : null;
  if (!analysis || !isFullAnalysis(analysis)) throw new Error(`fixture ${id} must be a full analysis`);
  return analysis;
};

const mount = (analysis: SessionAnalysisFull, props: Partial<Parameters<typeof SessionAnalysisTab>[0]> = {}) =>
  render(createElement(SessionAnalysisTab, { analysis, now: DETAIL_NOW, onOpenRequest: vi.fn(), ...props }));


export { flagshipFull, passA, mount };
