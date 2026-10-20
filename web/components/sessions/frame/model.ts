/**
 * 전역 프레임(A1~A6)의 순수 로직: 사용률 막대의 값과 문구, 미니 차트의 SVG 경로, 하단 저장소 상태.
 *
 * 원칙 하나: 모르는 값(null)은 0이 아니다. 사용률을 모르면 "0%"가 아니라 "?"(점선)이고, 미커밋 파일 수를 모르면
 * "미커밋 없음"이 아니라 "알 수 없음"이다. 관측한 0은 0으로 적는다.
 */

import type { AnalysisRepoStatus, FrameAccount, RepoStatus, SeriesBand, SeriesPoint, UsageWindowSeries } from '@/lib/session-analysis-types';

const isNum = (n: unknown): n is number => typeof n === 'number' && Number.isFinite(n);

/* ---------------- 사용률 막대 (A1) ---------------- */

type MeterLevel = 'low' | 'mid' | 'high' | 'unknown';
type MeterWindow = 'five_hour' | 'seven_day';

/** 이 값 이상이면 "주의", "위험"으로 본다. 한도에 가까워졌다는 신호일 뿐 평가가 아니다. */
const LEVEL_MID_FROM = 60;
const LEVEL_HIGH_FROM = 85;

/** 0~100으로 가둔다. 모르는 값(null, NaN)은 호출하는 쪽이 먼저 걸러야 한다 — 여기서 0이 되면 모름이 0으로 둔갑한다. */
const clampPct = (pct: number): number => (Number.isFinite(pct) ? Math.min(100, Math.max(0, pct)) : 0);

const meterLevel = (pct: number | null): MeterLevel => {
  if (!isNum(pct)) return 'unknown';
  if (pct >= LEVEL_HIGH_FROM) return 'high';
  if (pct >= LEVEL_MID_FROM) return 'mid';
  return 'low';
};

/** "99%". 0보다 크고 1보다 작으면 "<1%"(0으로 보이지 않게). 모르면 "?". */
const pctText = (pct: number | null): string => {
  if (!isNum(pct)) return '?';
  const p = clampPct(pct);
  if (p > 0 && p < 1) return '<1%';
  return `${Math.round(p)}%`;
};

/**
 * 재설정까지 남은 시간. 올림으로 센다(2시간 9분 31초는 "2시간 10분"): 줄어드는 숫자가 0에 닿기 전에
 * "재설정됨"으로 넘어가야 한다.
 */
const resetText = (iso: string | null, now: number): string => {
  if (!iso) return '재설정 시각을 알 수 없음';
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return '재설정 시각을 알 수 없음';
  const diff = at - now;
  if (diff <= 0) return '재설정됨';
  if (diff < 60_000) return '1분 안에 재설정';
  const minutes = Math.ceil(diff / 60_000);
  if (minutes < 60) return `${minutes}분 뒤 재설정`;
  const hours = Math.floor(minutes / 60);
  const restMinutes = minutes % 60;
  if (hours < 24) return restMinutes === 0 ? `${hours}시간 뒤 재설정` : `${hours}시간 ${restMinutes}분 뒤 재설정`;
  const days = Math.floor(hours / 24);
  const restHours = hours % 24;
  return restHours === 0 ? `${days}일 뒤 재설정` : `${days}일 ${restHours}시간 뒤 재설정`;
};

interface MeterRow {
  window: MeterWindow;
  label: '5시간' | '7일';
  /** 0~100. 모르면 null. */
  pct: number | null;
  level: MeterLevel;
  text: string;
  resetText: string;
  /** 툴팁과 접근성 이름: "5시간 창 99% · 2시간 10분 뒤 재설정". */
  title: string;
}

const WINDOW_LABEL: Record<MeterWindow, MeterRow['label']> = { five_hour: '5시간', seven_day: '7일' };

const meterRow = (window: MeterWindow, pct: number | null, resetsAt: string | null, now: number): MeterRow => {
  const value = isNum(pct) ? clampPct(pct) : null;
  const text = pctText(value);
  const label = WINDOW_LABEL[window];
  const reset = resetText(resetsAt, now);
  return {
    window,
    label,
    pct: value,
    level: meterLevel(value),
    text,
    resetText: reset,
    title: value === null ? `${label} 창 사용률을 알 수 없음 · ${reset}` : `${label} 창 ${text} · ${reset}`,
  };
};

/** 계정 하나의 두 줄: 위가 5시간 창, 아래가 7일 창(레퍼런스와 같은 순서). */
const meterRows = (account: FrameAccount, now: number): [MeterRow, MeterRow] => [
  meterRow('five_hour', account.five_hour_pct, account.resets_at.five_hour, now),
  meterRow('seven_day', account.seven_day_pct, account.resets_at.seven_day, now),
];

/** 계정 식별 색 자리. 같은 Claude 계정끼리 색이 겹치지 않게 순서로 돌려 쓰고, Codex는 자기 색이다. */
type IdentityKey = 'slot-0' | 'slot-1' | 'slot-2' | 'slot-3' | 'codex';
const CLAUDE_SLOTS = ['slot-0', 'slot-1', 'slot-2', 'slot-3'] as const;

const identityKey = (accounts: readonly FrameAccount[], index: number): IdentityKey => {
  if (accounts[index]?.provider === 'codex') return 'codex';
  const before = accounts.slice(0, index).filter((a) => a.provider === 'claude').length;
  return CLAUDE_SLOTS[before % CLAUDE_SLOTS.length];
};

/* ---------------- 미니 차트 (A3, F9) ---------------- */

interface ChartGeometry {
  width: number;
  height: number;
  padX: number;
  padY: number;
}

interface ChartPaths {
  /** 지난 창마다 한 줄. 그릴 점이 없는 창은 뺀다. */
  past: string[];
  median: string;
  /** 표본 부족 지점을 끊어 그린 25~75% 범위 다각형들. */
  band: string;
  current: string;
  hasPast: boolean;
  hasMedian: boolean;
  hasBand: boolean;
  hasCurrent: boolean;
}

const round1 = (n: number): number => Math.round(n * 10) / 10;
const fmt1 = (n: number): string => String(round1(n));
const clamp01 = (n: number): number => Math.min(1, Math.max(0, n));

const xOf = (fraction: number, g: ChartGeometry): number => g.padX + clamp01(fraction) * Math.max(0, g.width - 2 * g.padX);
/** 사용률이 높을수록 위(y가 작다). */
const yOf = (pct: number, g: ChartGeometry): number => g.height - g.padY - (clampPct(pct) / 100) * Math.max(0, g.height - 2 * g.padY);

const byX = <T extends { x: number }>(items: readonly T[]): T[] => [...items].sort((a, b) => a.x - b.x);

const finitePoints = (points: readonly SeriesPoint[]): SeriesPoint[] => byX(points.filter((p) => isNum(p.x) && isNum(p.y)));

const linePath = (points: readonly SeriesPoint[], g: ChartGeometry): string => {
  const pts = finitePoints(points).map((p) => `${fmt1(xOf(p.x, g))} ${fmt1(yOf(p.y, g))}`);
  if (pts.length === 0) return '';
  // 점이 하나면 길이 0의 선분을 그려 둥근 끝이 점으로 보이게 한다.
  if (pts.length === 1) return `M${pts[0]} L${pts[0]}`;
  return `M${pts[0]} ${pts
    .slice(1)
    .map((c) => `L${c}`)
    .join(' ')}`;
};

const splitAtGaps = <T extends { x: number }>(points: readonly T[], gapXs: readonly number[]): T[][] => {
  const segments: T[][] = [];
  let segment: T[] = [];
  const flush = () => {
    if (segment.length > 0) segments.push(segment);
    segment = [];
  };
  for (const point of byX(points)) {
    if (gapXs.includes(point.x)) {
      flush();
      continue;
    }
    const previous = segment.at(-1);
    if (previous && gapXs.some((x) => x > previous.x && x < point.x)) flush();
    segment.push(point);
  }
  flush();
  return segments;
};

const segmentedBandPath = (band: readonly SeriesBand[], gapXs: readonly number[], g: ChartGeometry): string =>
  splitAtGaps(band, gapXs).filter((points) => points.length >= 2).map((points) => {
    const upper = points.map((b) => `${fmt1(xOf(b.x, g))} ${fmt1(yOf(Math.max(b.low, b.high), g))}`);
    const lower = [...points].reverse().map((b) => `${fmt1(xOf(b.x, g))} ${fmt1(yOf(Math.min(b.low, b.high), g))}`);
    return `M${upper[0]} ${[...upper.slice(1), ...lower].map((c) => `L${c}`).join(' ')} Z`;
  }).join(' ');

const segmentedLinePath = (points: readonly SeriesPoint[], gapXs: readonly number[], g: ChartGeometry): string =>
  splitAtGaps(finitePoints(points), gapXs).map((segment) => linePath(segment, g)).filter(Boolean).join(' ');

const chartPaths = (series: UsageWindowSeries, geometry: ChartGeometry): ChartPaths => {
  const past = series.past.map((points) => linePath(points, geometry)).filter((d) => d !== '');
  const counts = (series.sampleCounts ?? []).filter((point) => Number.isSafeInteger(point.n) && point.n >= 0);
  const median = segmentedLinePath(series.median, counts.filter((point) => point.n === 0).map((point) => point.x), geometry);
  const band = segmentedBandPath(series.band, counts.filter((point) => point.n < 4).map((point) => point.x), geometry);
  const current = linePath(series.current, geometry);
  return {
    past,
    median,
    band,
    current,
    hasPast: past.length > 0,
    hasMedian: median !== '',
    hasBand: band !== '',
    hasCurrent: current !== '',
  };
};

interface ChartSample {
  x: number;
  n: number | null;
  current: number | null;
  median: number | null;
  low: number | null;
  high: number | null;
  past: (number | null)[];
}

/** 계열의 실제 관측 위치를 합친다. 전달되지 않은 백분율은 보간해 만들지 않는다. */
const chartSamples = (series: UsageWindowSeries): ChartSample[] => {
  const valid = (points: readonly SeriesPoint[]): SeriesPoint[] => finitePoints(points)
    .filter((point) => point.x >= 0 && point.x <= 1)
    .map((point) => ({ x: point.x, y: clampPct(point.y) }));
  const current = valid(series.current);
  const median = valid(series.median);
  const past = series.past.map(valid);
  const band = byX(series.band.filter((point) =>
    isNum(point.x) && point.x >= 0 && point.x <= 1 && isNum(point.low) && isNum(point.high),
  ));
  const sampleCounts = byX((series.sampleCounts ?? []).filter((point) =>
    isNum(point.x) && point.x >= 0 && point.x <= 1 && Number.isSafeInteger(point.n) && point.n >= 0,
  ));
  const positions = [...new Set([...current, ...median, ...past.flat(), ...band, ...sampleCounts].map((point) => point.x))].sort((a, b) => a - b);
  const valueAt = (points: readonly SeriesPoint[], x: number): number | null => points.find((point) => point.x === x)?.y ?? null;
  const low = band.map((point) => ({ x: point.x, y: clampPct(Math.min(point.low, point.high)) }));
  const high = band.map((point) => ({ x: point.x, y: clampPct(Math.max(point.low, point.high)) }));
  return positions.map((x) => ({
    x,
    n: sampleCounts.find((point) => point.x === x)?.n ?? null,
    current: valueAt(current, x),
    median: sampleCounts.find((point) => point.x === x)?.n === 0 ? null : valueAt(median, x),
    low: (sampleCounts.find((point) => point.x === x)?.n ?? 4) < 4 ? null : valueAt(low, x),
    high: (sampleCounts.find((point) => point.x === x)?.n ?? 4) < 4 ? null : valueAt(high, x),
    past: past.map((points) => valueAt(points, x)),
  }));
};

const SERIES_WINDOW_NAME: Record<UsageWindowSeries['window'], string> = { five_hour: '5시간 창', seven_day: '7일 창' };

/** 차트의 접근성 설명. 지금 창의 마지막 값(가장 늦은 점)과 비교한 지난 창의 수를 말한다. */
const chartLabel = (series: UsageWindowSeries, accountLabel: string): string => {
  const last = finitePoints(series.current).at(-1);
  const counts = (series.sampleCounts ?? []).filter((point) => Number.isSafeInteger(point.n) && point.n >= 0);
  const historyCount = series.historyCount ?? series.past.length;
  const hasHistory = series.past.length > 0 || series.median.length > 0 || series.band.length > 0 || historyCount > 0;
  const subject = `${accountLabel ? `${accountLabel} ` : ''}${SERIES_WINDOW_NAME[series.window]} 사용률`;
  const current = last ? `지금 창은 ${pctText(last.y)}입니다.` : '지금 창의 값이 없습니다.';
  const past = hasHistory
    ? `지난 창${historyCount > 0 ? ` ${historyCount}개` : ''}의 관측을 비교합니다.${series.median.length > 0 ? ' 중앙값은 점선입니다.' : ''}`
    : '비교할 지난 창이 없습니다.';
  const insufficient = [...new Set(counts.filter((point) => point.n < 4).map((point) => point.n))].sort((a, b) => a - b);
  const band = insufficient.length > 0
    ? `표본 수 ${insufficient.join(', ')}인 지점의 25~75% 범위는 판단 불가입니다.`
    : counts.length === 0
      ? '25~75% 범위 표본 수를 판단할 수 없는 지점은 알 수 없음으로 표시합니다.'
      : '25~75% 범위는 표본 4개 이상인 지점에서 표시합니다.';
  return `${subject}. ${current} ${past} ${band}`;
};

/* ---------------- 저장소 상태 (A6) ---------------- */

type RepoTone = 'clean' | 'dirty' | 'unknown';

interface FrameRepoStatus {
  branch: string | null;
  uncommitted: number | null;
  source?: AnalysisRepoStatus['uncommitted_source'];
  scope?: AnalysisRepoStatus['uncommitted_scope'];
  reason?: AnalysisRepoStatus['uncommitted_reason'];
}

interface RepoStatusText {
  branch: string;
  uncommitted: string;
  tone: RepoTone;
}

const countFormat = new Intl.NumberFormat('en-US');

const repoStatusText = (repo: FrameRepoStatus): RepoStatusText => {
  const branch = repo.branch === null ? '브랜치 알 수 없음' : repo.branch.trim() || '브랜치 없음';
  const n = repo.uncommitted;
  if (!isNum(n) || n < 0) return { branch, uncommitted: '미커밋 알 수 없음', tone: 'unknown' };
  if (n === 0) return { branch, uncommitted: '미커밋 없음', tone: 'clean' };
  return { branch, uncommitted: `미커밋 ${countFormat.format(n)}건`, tone: 'dirty' };
};

const repoStatusFromAnalysis = (status?: AnalysisRepoStatus): FrameRepoStatus => ({
  branch: status?.branch_state === 'observed' ? status.branch : null,
  uncommitted: status?.uncommitted_state === 'observed' ? status.uncommitted_edited_files : null,
  source: status?.uncommitted_source ?? 'unknown',
  scope: status?.uncommitted_scope,
  reason: status?.uncommitted_reason,
});

const repoStatusDescription = (repo: FrameRepoStatus): string => {
  const source = repo.source === 'session_git_snapshot' ? '출처: 세션 Git 스냅샷' : '출처: 알 수 없음';
  const scope = repo.scope === 'session_edited_files' ? '범위: 이 세션 편집 파일' : '';
  const reasons: Record<NonNullable<FrameRepoStatus['reason']>, string> = {
    no_git_snapshot: 'Git 스냅샷 없음',
    truncated_snapshot: 'Git 스냅샷이 잘렸음',
    partial_snapshot: 'Git 스냅샷이 일부임',
    unresolved_git_paths: '미해결 Git 경로가 있음',
    invalid_git_snapshot: 'Git 스냅샷이 유효하지 않음',
  };
  return [source, scope, repo.reason ? reasons[repo.reason] : ''].filter(Boolean).join(' · ');
};

export {
  clampPct,
  meterLevel,
  pctText,
  resetText,
  meterRows,
  identityKey,
  chartPaths,
  chartSamples,
  chartLabel,
  repoStatusText,
  repoStatusFromAnalysis,
  repoStatusDescription,
};
export type { MeterLevel, MeterWindow, MeterRow, IdentityKey, ChartGeometry, ChartPaths, ChartSample, RepoTone, RepoStatusText, FrameRepoStatus };
