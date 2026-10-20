export { FrameTopBar } from './top-bar';
export { FrameStatusBar } from './status-bar';
export { UsageMeters } from './usage-meters';
export { UsageMiniChart, CHART_GEOMETRY } from './usage-mini-chart';
export { GlobalSearch } from './global-search';
export {
  clampPct,
  meterLevel,
  pctText,
  resetText,
  meterRows,
  identityKey,
  chartPaths,
  chartLabel,
  repoStatusText,
} from './model';
export type { MeterLevel, MeterWindow, MeterRow, IdentityKey, ChartGeometry, ChartPaths, RepoTone, RepoStatusText } from './model';
export { searchSessions, FIELD_ORDER } from './search';
export type { SearchDoc, SearchField, SearchHit } from './search';
