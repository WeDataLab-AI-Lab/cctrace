/**
 * Fixed-week periods for the weekly report.
 *
 * A week is identified as "2026-W37" (ISO 8601: weeks start on Monday, week 1
 * holds the year's first Thursday) and covers Monday 00:00 to the next Monday
 * 00:00 in the viewer's time zone. The calendar arithmetic runs on UTC dates so
 * it never sees DST; only the conversion of a local midnight to an instant
 * consults the time zone, which is why a week can last 167 or 169 hours.
 */

const DAY_MS = 86400 * 1000;
const WEEK_ID_PATTERN = /^(\d{4})-W(\d{2})$/;

interface WeekRange {
  since: string;
  until: string;
}

/** Monday of ISO week 1, as a UTC-midnight timestamp. */
const firstMonday = (year: number): number => {
  const jan4 = Date.UTC(year, 0, 4);
  return jan4 - ((new Date(jan4).getUTCDay() + 6) % 7) * DAY_MS;
};

/** ISO week identifier of a UTC-midnight calendar date. */
const weekIdOfDate = (date: number): string => {
  const weekday = (new Date(date).getUTCDay() + 6) % 7;
  const thursday = new Date(date + (3 - weekday) * DAY_MS);
  const year = thursday.getUTCFullYear();
  const dayOfYear = (thursday.getTime() - Date.UTC(year, 0, 1)) / DAY_MS;
  const week = Math.floor(dayOfYear / 7) + 1;
  return `${year}-W${String(week).padStart(2, '0')}`;
};

/** Monday of a valid week identifier, as a UTC-midnight timestamp; null otherwise. */
const mondayOf = (text: string): number | null => {
  const match = WEEK_ID_PATTERN.exec(text);
  if (!match) return null;
  const year = Number(match[1]);
  const week = Number(match[2]);
  if (week < 1) return null;
  const monday = firstMonday(year) + (week - 1) * 7 * DAY_MS;
  // Week 53 exists only in some years; past the last week the date rolls into the next year.
  if (weekIdOfDate(monday) !== text) return null;
  return monday;
};

const requireMonday = (weekId: string): number => {
  const monday = mondayOf(weekId);
  if (monday === null) throw new Error(`invalid week id: ${weekId}`);
  return monday;
};

/** Calendar fields of an instant as seen in the time zone. */
const zonedParts = (at: number, timeZone: string): Record<string, number> => {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone,
    hourCycle: 'h23',
    year: 'numeric',
    month: 'numeric',
    day: 'numeric',
    hour: 'numeric',
    minute: 'numeric',
    second: 'numeric',
  }).formatToParts(at);
  return Object.fromEntries(parts.filter((p) => p.type !== 'literal').map((p) => [p.type, Number(p.value)]));
};

/** Offset of the time zone from UTC at an instant, in milliseconds. */
const zoneOffset = (at: number, timeZone: string): number => {
  const p = zonedParts(at, timeZone);
  const wall = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second);
  return wall - Math.floor(at / 1000) * 1000;
};

/** Instant at which a wall-clock time (given as a UTC timestamp) occurs in the time zone. */
const zonedToInstant = (wall: number, timeZone: string): number => {
  const guess = wall - zoneOffset(wall, timeZone);
  return wall - zoneOffset(guess, timeZone);
};

const parseWeekId = (text: string): string | null => (mondayOf(text) === null ? null : text);

const currentWeekId = (now: number, timeZone: string): string => {
  const p = zonedParts(now, timeZone);
  return weekIdOfDate(Date.UTC(p.year, p.month - 1, p.day));
};

const weekRange = (weekId: string, timeZone: string): WeekRange => {
  const monday = requireMonday(weekId);
  return {
    since: new Date(zonedToInstant(monday, timeZone)).toISOString(),
    until: new Date(zonedToInstant(monday + 7 * DAY_MS, timeZone)).toISOString(),
  };
};

const shiftWeek = (weekId: string, delta: number): string =>
  weekIdOfDate(requireMonday(weekId) + delta * 7 * DAY_MS);

const isFutureWeek = (weekId: string, now: number, timeZone: string): boolean =>
  new Date(weekRange(weekId, timeZone).since).getTime() > now;

const formatWeekLabel = (weekId: string, timeZone: string): string => {
  const { since, until } = weekRange(weekId, timeZone);
  const monthDay = (at: number): string => {
    const p = zonedParts(at, timeZone);
    return `${p.month}/${p.day}`;
  };
  return `${monthDay(new Date(since).getTime())}–${monthDay(new Date(until).getTime() - 1)}`;
};

export { currentWeekId, formatWeekLabel, isFutureWeek, parseWeekId, shiftWeek, weekRange };
export type { WeekRange };
