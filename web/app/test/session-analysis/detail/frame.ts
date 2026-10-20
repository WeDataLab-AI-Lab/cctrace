/**
 * 전역 프레임(A1~A6)의 합성 데이터: 계정 셋의 5시간·7일 사용률, 선택한 계정의 창 겹침 차트(지금 창, 지난 창들, 중앙값,
 * 25~75% 범위), 저장소 상태. 곡선은 시드가 있는 난수로 만들어 늘 같다.
 *
 * 사용률을 모르는 값은 만들지 않는다 — 여기 숫자는 모두 "관측한 값"의 흉내다. 0%인 계정은 관측한 0이다.
 */

import type {
  FrameAccount,
  SeriesBand,
  SeriesPoint,
  SessionFrame,
  UsageWindowSeries,
} from '@/lib/session-analysis-types';
import { DETAIL_NOW } from './builders';

/** 화면에 보이는 계정 이름. 라디오는 Claude 계정(메인, 서브)에만 있다. */
export const FRAME_ACCOUNT_LABELS: readonly string[] = ['메인', '서브', 'Codex'];

const DEFAULT_SELECTED = '서브';

/* ---------------- 난수와 곡선 ---------------- */

/** 32비트 시드에서 0 이상 1 미만의 수열을 만든다. */
export const mulberry32 = (seed: number): (() => number) => {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4_294_967_296;
  };
};

const seedOf = (text: string): number => {
  let h = 2_166_136_261;
  for (let i = 0; i < text.length; i += 1) h = Math.imul(h ^ text.charCodeAt(i), 16_777_619);
  return h >>> 0;
};

const round1 = (n: number): number => Math.round(n * 10) / 10;
const round3 = (n: number): number => Math.round(n * 1_000) / 1_000;

/**
 * 0에서 시작해 `end`까지 오르는 줄어들지 않는 곡선. x는 0부터 `xMax`까지 `steps`칸이다. 곡선의 모양(앞이 가파른가 뒤가
 * 가파른가)과 작은 흔들림은 난수가 정한다. y는 0~100 안이다.
 */
export const monotoneCurve = (rng: () => number, end: number, xMax: number, steps: number): SeriesPoint[] => {
  const power = 0.8 + rng() * 0.9;
  const points: SeriesPoint[] = [];
  let prev = 0;
  for (let i = 0; i <= steps; i += 1) {
    const base = end * Math.pow(i / steps, power);
    const jitter = i === 0 || i === steps ? 0 : (rng() - 0.5) * end * 0.04;
    const y = i === steps ? Math.max(prev, end) : Math.min(100, Math.max(prev, base + jitter));
    points.push({ x: round3((i / steps) * xMax), y: round1(Math.min(100, y)) });
    prev = y;
  }
  return points;
};

/** 오름차순 점열 위의 x에서 선형으로 읽은 값. 범위 밖은 양 끝 값이다. 점이 없으면 null이다. */
export const valueAt = (curve: readonly SeriesPoint[], x: number): number | null => {
  if (curve.length === 0) return null;
  if (x <= curve[0].x) return curve[0].y;
  const last = curve[curve.length - 1];
  if (x >= last.x) return last.y;
  for (let i = 1; i < curve.length; i += 1) {
    const hi = curve[i];
    if (x <= hi.x) {
      const lo = curve[i - 1];
      const span = hi.x - lo.x;
      return span === 0 ? hi.y : lo.y + ((hi.y - lo.y) * (x - lo.x)) / span;
    }
  }
  return last.y;
};

/** 선형 보간 백분위수(0~1). 값이 없으면 null이다(0이 아니다). */
export const percentile = (values: readonly number[], p: number): number | null => {
  if (values.length === 0) return null;
  const sorted = [...values].sort((a, b) => a - b);
  const pos = (sorted.length - 1) * Math.min(1, Math.max(0, p));
  const lo = Math.floor(pos);
  const hi = Math.ceil(pos);
  return sorted[lo] + (sorted[hi] - sorted[lo]) * (pos - lo);
};

const GRID_POINTS = 24;

/** 지난 창들의 같은 x에서의 중앙값 선과 25~75% 범위. */
export const medianAndBand = (past: readonly (readonly SeriesPoint[])[]): { median: SeriesPoint[]; band: SeriesBand[] } => {
  const median: SeriesPoint[] = [];
  const band: SeriesBand[] = [];
  for (let k = 0; k < GRID_POINTS; k += 1) {
    const x = round3(k / (GRID_POINTS - 1));
    const ys = past.flatMap((curve) => {
      const y = valueAt(curve, x);
      return y === null ? [] : [y];
    });
    const mid = percentile(ys, 0.5);
    const low = percentile(ys, 0.25);
    const high = percentile(ys, 0.75);
    if (mid === null || low === null || high === null) continue;
    median.push({ x, y: round1(mid) });
    band.push({ x, low: round1(low), high: round1(high) });
  }
  return { median, band };
};

/* ---------------- 계정 ---------------- */

interface AccountSpec {
  label: string;
  provider: FrameAccount['provider'];
  five: number;
  seven: number;
  /** 창이 다시 시작될 때까지 남은 분. 창 길이(5시간=300분, 7일=10,080분)에서 지난 비율이 정해진다. */
  resetFive: number;
  resetSeven: number;
  /** 과거 사용률 분포를 정하는 기준 끝 값. 이 값 사이를 보간해 5시간 36개, 7일 17개 창을 만든다. */
  pastFive: readonly number[];
  pastSeven: readonly number[];
}

const ACCOUNTS: readonly AccountSpec[] = [
  { label: '메인', provider: 'claude', five: 0, seven: 99, resetFive: 165, resetSeven: 4_900, pastFive: [22, 35, 47, 58, 66, 78], pastSeven: [55, 68, 80, 91, 100] },
  { label: '서브', provider: 'claude', five: 1, seven: 17, resetFive: 150, resetSeven: 5_200, pastFive: [30, 42, 53, 63, 72, 85], pastSeven: [28, 40, 52, 63, 75] },
  { label: 'Codex', provider: 'codex', five: 0, seven: 88, resetFive: 120, resetSeven: 4_500, pastFive: [12, 20, 31, 40, 52, 60], pastSeven: [48, 62, 71, 83, 95] },
];

const FIVE_HOUR_MINUTES = 300;
const SEVEN_DAY_MINUTES = 10_080;
const FIVE_HOUR_PAST_COUNT = 36;
const SEVEN_DAY_PAST_COUNT = 17;

/** 기준 사용률 사이를 오가되 양 끝을 유지하는 합성 이력. 시드 난수는 파형과 각 곡선 모양에만 쓴다. */
const pastCurves = (spec: AccountSpec, window: 'five_hour' | 'seven_day'): SeriesPoint[][] => {
  const anchors = window === 'five_hour' ? spec.pastFive : spec.pastSeven;
  const count = window === 'five_hour' ? FIVE_HOUR_PAST_COUNT : SEVEN_DAY_PAST_COUNT;
  const rng = mulberry32(seedOf(`${spec.label}:${window}:past`));
  return Array.from({ length: count }, (_, index) => {
    const scaled = (index / (count - 1)) * (anchors.length - 1);
    const low = Math.floor(scaled);
    const high = Math.min(low + 1, anchors.length - 1);
    const blend = scaled - low;
    const end = round1(anchors[low] + (anchors[high] - anchors[low]) * blend);
    return monotoneCurve(rng, end, 1, 24);
  });
};

const inMinutes = (minutes: number): string => new Date(DETAIL_NOW + minutes * 60_000).toISOString();

const seriesOf = (window: 'five_hour' | 'seven_day', spec: AccountSpec): UsageWindowSeries => {
  const five = window === 'five_hour';
  const length = five ? FIVE_HOUR_MINUTES : SEVEN_DAY_MINUTES;
  const reset = five ? spec.resetFive : spec.resetSeven;
  const elapsed = round3(1 - reset / length);
  const rng = mulberry32(seedOf(`${spec.label}:${window}`));
  const current = monotoneCurve(rng, five ? spec.five : spec.seven, elapsed, 12);
  const past = pastCurves(spec, window);
  const { median, band } = medianAndBand(past);
  return { window, current, past, median, band };
};

const accountOf = (spec: AccountSpec, selected: boolean): FrameAccount => ({
  label: spec.label,
  provider: spec.provider,
  five_hour_pct: spec.five,
  seven_day_pct: spec.seven,
  resets_at: { five_hour: inMinutes(spec.resetFive), seven_day: inMinutes(spec.resetSeven) },
  selected,
});

/** 고른 Claude 계정의 프레임. 알 수 없는 이름이거나 Codex이면 기본 계정("서브")이다 — Codex에는 라디오가 없다. */
export const frameFor = (selectedLabel: string): SessionFrame => {
  const fallback = ACCOUNTS.find((a) => a.label === DEFAULT_SELECTED) ?? ACCOUNTS[0];
  const chosen = ACCOUNTS.find((a) => a.provider === 'claude' && a.label === selectedLabel) ?? fallback;
  return {
    accounts: ACCOUNTS.map((a) => accountOf(a, a.label === chosen.label)),
    series: { five_hour: seriesOf('five_hour', chosen), seven_day: seriesOf('seven_day', chosen) },
    repo: { branch: 'feat/inbox-filters', uncommitted: 5 },
  };
};
