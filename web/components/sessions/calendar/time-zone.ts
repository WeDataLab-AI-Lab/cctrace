/**
 * IANA 시간대를 다루는 순수 함수. 캘린더의 날짜 열은 "지역 자정부터 다음 지역 자정까지"의 **절대 시간 구간**이라,
 * 지역 시각과 절대 시각(ms)을 오가는 일이 전부 여기에 있다.
 *
 * `Intl.DateTimeFormat`만 쓴다(의존성 없음). 형식 객체는 시간대마다 하나만 만든다 — 만드는 비용이 크다.
 *
 * 지역 날짜는 `LocalDate`({year, month, day})로 다룬다. 시각이 아니라 달력의 한 칸이다.
 */

export interface LocalDate {
  year: number;
  /** 1~12 */
  month: number;
  day: number;
}

export interface ZonedParts {
  year: number;
  month: number;
  day: number;
  hour: number;
  minute: number;
  second: number;
}

const MINUTE_MS = 60_000;
const HOUR_MS = 3_600_000;
const DAY_MS = 86_400_000;

const pad2 = (n: number): string => String(n).padStart(2, '0');

/* ---------------- 지역 날짜 (달력 산술) ---------------- */

const dateKey = (d: LocalDate): string => `${String(d.year).padStart(4, '0')}-${pad2(d.month)}-${pad2(d.day)}`;

const parseDateKey = (key: string): LocalDate | null => {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(key);
  if (!m) return null;
  const date = { year: Number(m[1]), month: Number(m[2]), day: Number(m[3]) };
  // 2월 30일 같은 값은 날짜가 아니다.
  const back = new Date(Date.UTC(date.year, date.month - 1, date.day));
  return back.getUTCFullYear() === date.year && back.getUTCMonth() === date.month - 1 && back.getUTCDate() === date.day ? date : null;
};

/** 달력 날짜를 일 수만큼 옮긴다. 시간대와 무관하다(시각이 아니라 날짜의 산술이다). */
const addDays = (d: LocalDate, n: number): LocalDate => {
  const t = new Date(Date.UTC(d.year, d.month - 1, d.day + n));
  return { year: t.getUTCFullYear(), month: t.getUTCMonth() + 1, day: t.getUTCDate() };
};

/** 0 = 일요일 ... 6 = 토요일. */
const weekdayOf = (d: LocalDate): number => new Date(Date.UTC(d.year, d.month - 1, d.day)).getUTCDay();

/** 그 날짜가 든 주의 월요일(캘린더는 월요일에 시작한다, C6). */
const mondayOf = (d: LocalDate): LocalDate => addDays(d, -((weekdayOf(d) + 6) % 7));

const sameDate = (a: LocalDate, b: LocalDate): boolean => a.year === b.year && a.month === b.month && a.day === b.day;

/** 날짜를 정수 하나로(대소 비교용). */
const dayNumber = (d: LocalDate): number => Date.UTC(d.year, d.month - 1, d.day) / DAY_MS;

/* ---------------- 시간대 형식 ---------------- */

const formatters = new Map<string, Intl.DateTimeFormat>();

const formatterFor = (timeZone: string): Intl.DateTimeFormat => {
  const cached = formatters.get(timeZone);
  if (cached) return cached;
  const created = new Intl.DateTimeFormat('en-US', {
    timeZone,
    hourCycle: 'h23',
    year: 'numeric',
    month: 'numeric',
    day: 'numeric',
    hour: 'numeric',
    minute: 'numeric',
    second: 'numeric',
  });
  formatters.set(timeZone, created);
  return created;
};

/** 브라우저가 이해하는 IANA 이름인가. */
const isValidTimeZone = (timeZone: string): boolean => {
  if (typeof timeZone !== 'string' || !timeZone.trim()) return false;
  try {
    formatterFor(timeZone);
    return true;
  } catch {
    return false;
  }
};

/** 유효하지 않은 이름은 명시적인 UTC 기준으로 대신한다. 브라우저의 암묵적인 로컬 시간대는 쓰지 않는다. */
const calendarTimeZone = (timeZone: string): string => isValidTimeZone(timeZone) ? timeZone : 'UTC';

/** 절대 시각(ms)을 그 시간대의 지역 시각 성분으로. */
const partsAt = (timeZone: string, ms: number): ZonedParts => {
  const parts = formatterFor(timeZone).formatToParts(new Date(ms));
  const get = (type: Intl.DateTimeFormatPartTypes): number => {
    const found = parts.find((p) => p.type === type);
    return found ? Number(found.value) : 0;
  };
  return {
    year: get('year'),
    month: get('month'),
    day: get('day'),
    // 일부 환경은 자정을 24로 적는다.
    hour: get('hour') % 24,
    minute: get('minute'),
    second: get('second'),
  };
};

const localDateOf = (timeZone: string, ms: number): LocalDate => {
  const p = partsAt(timeZone, ms);
  return { year: p.year, month: p.month, day: p.day };
};

/** 그 순간의 UTC 오프셋(분). 동쪽이 양수다(서울 +540, 뉴욕 여름 -240). */
const offsetMinutesAt = (timeZone: string, ms: number): number => {
  const p = partsAt(timeZone, ms);
  const asUtc = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second);
  const wholeSecond = Math.floor(ms / 1000) * 1000;
  return Math.round((asUtc - wholeSecond) / MINUTE_MS);
};

/** "UTC+9", "UTC-4", "UTC+5:30". */
const offsetLabel = (offsetMinutes: number): string => {
  if (offsetMinutes === 0) return 'UTC';
  const sign = offsetMinutes > 0 ? '+' : '-';
  const abs = Math.abs(offsetMinutes);
  const h = Math.floor(abs / 60);
  const m = abs % 60;
  return `UTC${sign}${h}${m === 0 ? '' : `:${pad2(m)}`}`;
};

/**
 * 그 지역 날짜가 시작되는 절대 시각(ms): 지역 날짜가 그 날 이상인 **가장 이른** 순간.
 *
 * 자정이 없는 날(자정에 시계가 건너뛰는 시간대)은 건너뛴 직후가 그 날의 시작이다. 지역 날짜는 시간이 지나도
 * 줄어들지 않으므로(자정에 시계를 되돌리는 시간대도 되돌린 시각이 전날 23시라 날짜 자체는 늘어난 적이 없다)
 * 분 단위 이분 탐색으로 찾는다.
 */
const zonedMidnight = (timeZone: string, date: LocalDate): number => {
  const target = dayNumber(date);
  const utcMidnightMin = Date.UTC(date.year, date.month - 1, date.day) / MINUTE_MS;
  // 지역 시각 = 절대 시각 + 오프셋이고 오프셋은 -12시간~+14시간이다. 지역 자정의 절대 시각은 UTC 자정에서
  // -14시간~+12시간 안이므로, 한 시간씩 넉넉히 잡은 [-15h, +13h]의 앞 끝은 전날, 뒤 끝은 그 날 이후다.
  let lo = utcMidnightMin - 15 * 60;
  let hi = utcMidnightMin + 13 * 60;
  const dayNumberAt = (min: number): number => {
    const p = partsAt(timeZone, min * MINUTE_MS);
    return Date.UTC(p.year, p.month - 1, p.day) / DAY_MS;
  };
  // 불변식: dayNumberAt(lo) < target <= dayNumberAt(hi)
  while (hi - lo > 1) {
    const mid = Math.floor((lo + hi) / 2);
    if (dayNumberAt(mid) >= target) hi = mid;
    else lo = mid;
  }
  return hi * MINUTE_MS;
};

/* ---------------- 지역 시각 표기 ---------------- */

/** HH:MM (그 시간대). */
const formatClockIn = (timeZone: string, ms: number): string => {
  const p = partsAt(timeZone, ms);
  return `${pad2(p.hour)}:${pad2(p.minute)}`;
};

/** M/D (그 시간대). */
const formatMonthDayIn = (timeZone: string, ms: number): string => {
  const p = partsAt(timeZone, ms);
  return `${p.month}/${p.day}`;
};

/** MM/DD HH:MM (그 시간대). */
const formatDateTimeIn = (timeZone: string, ms: number): string => {
  const p = partsAt(timeZone, ms);
  return `${pad2(p.month)}/${pad2(p.day)} ${pad2(p.hour)}:${pad2(p.minute)}`;
};

export {
  HOUR_MS,
  MINUTE_MS,
  DAY_MS,
  dateKey,
  parseDateKey,
  addDays,
  weekdayOf,
  mondayOf,
  sameDate,
  dayNumber,
  isValidTimeZone,
  calendarTimeZone,
  partsAt,
  localDateOf,
  offsetMinutesAt,
  offsetLabel,
  zonedMidnight,
  formatClockIn,
  formatMonthDayIn,
  formatDateTimeIn,
};
