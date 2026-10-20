/**
 * 처음 그릴 때 격자를 어느 시각으로 스크롤할지. 하루가 24시간이라 열 높이의 위쪽 절반이 비어 있는 주가 흔하므로,
 * 그 주 데이터의 **일한 시간대**의 시작으로 스크롤한다.
 *
 * 일한 시간대 = 그 주의 띠들이 지역 시각별로 차지한 시간을 모아, 충분한 활동이 반복되는 시부터 센다. 한두 개의 새벽
 * 세션이 시작 시각을 끌어당기지 않게 하려는 것이다. 흐린(범위만 그리는) 띠는 활동이 아니라 범위라서 뺀다.
 * 활동이 하나도 없으면 업무 시간 8~19시를 기본으로 쓴다.
 */

import { HOUR_MS } from './time-zone';

const DEFAULT_FIRST_HOUR = 8;
const DEFAULT_LAST_HOUR = 19;
const MIN_HOUR_SHARE = 0.08;
const MIN_PEAK_SHARE = 0.6;
/** 길이 0인 띠(기록 한 줄)가 가지는 무게(ms). 활동이 있었다는 사실이라 0은 아니지만 작다. */
const ZERO_LENGTH_WEIGHT = 60_000;
/** 가을 전환일은 25시간이라 시 순번이 24까지 간다. */
const LAST_HOUR_INDEX = 24;

export interface WorkingHoursBand {
  start: number;
  end: number;
  faint: boolean;
}

export interface WorkingHoursDay {
  /** 지역 자정의 절대 시각(ms). */
  start: number;
  bands: readonly WorkingHoursBand[];
}

export interface WorkingHours {
  /** 일한 시간대의 첫 시(자정부터 흐른 시간의 정수 부분). */
  firstHour: number;
  lastHour: number;
  /** 활동이 하나도 없어 기본값을 썼다. */
  empty: boolean;
}

const hourIndex = (day: WorkingHoursDay, at: number): number =>
  Math.min(LAST_HOUR_INDEX, Math.max(0, Math.floor((at - day.start) / HOUR_MS)));

const workingHours = (days: readonly WorkingHoursDay[]): WorkingHours => {
  const weight = new Array<number>(LAST_HOUR_INDEX + 1).fill(0);
  let total = 0;
  for (const day of days) {
    for (const band of day.bands) {
      if (band.faint) continue;
      if (band.end <= band.start) {
        weight[hourIndex(day, band.start)] += ZERO_LENGTH_WEIGHT;
        total += ZERO_LENGTH_WEIGHT;
        continue;
      }
      for (let h = hourIndex(day, band.start); h <= hourIndex(day, band.end - 1); h++) {
        const from = Math.max(band.start, day.start + h * HOUR_MS);
        const to = Math.min(band.end, day.start + (h + 1) * HOUR_MS);
        const overlap = Math.max(0, to - from);
        weight[h] += overlap;
        total += overlap;
      }
    }
  }
  if (total <= 0) return { firstHour: DEFAULT_FIRST_HOUR, lastHour: DEFAULT_LAST_HOUR, empty: true };

  const peak = Math.max(...weight);
  const active = weight.map((amount, hour) => ({ amount, hour })).filter(({ amount }) =>
    amount > 0 && (amount / total >= MIN_HOUR_SHARE || amount / peak >= MIN_PEAK_SHARE));
  // 활동이 고르게 매우 작은 시간대에 흩어졌다면 첫 활동도 버리지 않는다.
  const firstHour = active[0]?.hour ?? weight.findIndex((amount) => amount > 0);
  const lastHour = active[active.length - 1]?.hour ?? weight.findLastIndex((amount) => amount > 0);
  return { firstHour, lastHour, empty: false };
};

/** 처음 스크롤할 시(한 시간 앞의 여유를 둔다). */
const initialScrollHour = (hours: WorkingHours): number => Math.max(0, hours.firstHour - 1);

export { DEFAULT_FIRST_HOUR, DEFAULT_LAST_HOUR, workingHours, initialScrollHour };
