type TrendGranularity = 'minute' | 'hour' | 'day' | 'week' | 'month';
type TrendTickInterval = number | 'preserveStartEnd';

/**
 * Formats an x-axis tick using the Cost Trend chart's labels.
 *
 * Cost Trend supplies bucket keys while Subscription Burn supplies epoch
 * milliseconds. Numeric values are converted to the same local calendar parts
 * before applying the shared label rules.
 */
const formatTrendTick = (granularity: TrendGranularity, value: string | number): string => {
  if (typeof value === 'string') {
    if (granularity === 'day' || granularity === 'week') return value.slice(5);
    if (granularity === 'month') return value.slice(0, 7);
  }

  const date = new Date(value);
  const year = String(date.getFullYear());
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  if (granularity === 'day' || granularity === 'week') return `${month}-${day}`;
  if (granularity === 'month') return `${year}-${month}`;

  const hour = String(date.getHours()).padStart(2, '0');
  const minute = String(date.getMinutes()).padStart(2, '0');
  if (granularity === 'minute') return `${hour}:${minute}`;
  return `${month}/${day} ${hour}h`;
};

/** Cost Trend shows about twelve minute/short-zoom labels; coarser axes let
 * Recharts preserve the first and last labels while removing collisions. */
const trendTickInterval = (
  granularity: TrendGranularity,
  pointCount: number,
  zoomed = false,
): TrendTickInterval => {
  if (granularity === 'minute' || (zoomed && pointCount < 60)) {
    return Math.max(1, Math.floor(pointCount / 12));
  }
  return 'preserveStartEnd';
};

/**
 * Bucket boundaries and keys, shared by both charts on the Overview page.
 *
 * These lived privately inside trend-chart.tsx. The burn chart has to bucket by
 * the SAME keys or the two plots put the same instant at a different x, which
 * defeats reading them together -- and a component is the wrong place to import
 * that from, so they sit beside the axis labels that already serve both.
 *
 * Local calendar parts throughout, deliberately. The server buckets with
 * date_trunc(granularity, ts AT TIME ZONE tz) where tz comes from the browser,
 * so local time is what the keys have to agree with.
 */
const alignBucketStart = (cur: Date, g: TrendGranularity): void => {
  if (g === 'minute') { cur.setSeconds(0, 0); return; }
  if (g === 'hour') { cur.setMinutes(0, 0, 0); return; }
  if (g === 'day') { cur.setHours(0, 0, 0, 0); return; }
  if (g === 'week') {
    const dow = cur.getDay();
    cur.setDate(cur.getDate() - (dow === 0 ? 6 : dow - 1));
    cur.setHours(0, 0, 0, 0);
    return;
  }
  cur.setDate(1);
  cur.setHours(0, 0, 0, 0);
};

const advanceBucket = (cur: Date, g: TrendGranularity): void => {
  if (g === 'minute') { cur.setMinutes(cur.getMinutes() + 1); return; }
  if (g === 'hour') { cur.setHours(cur.getHours() + 1); return; }
  if (g === 'day') { cur.setDate(cur.getDate() + 1); return; }
  if (g === 'week') { cur.setDate(cur.getDate() + 7); return; }
  cur.setMonth(cur.getMonth() + 1);
};

const bucketKey = (d: Date, g: TrendGranularity): string => {
  const y = d.getFullYear();
  const mo = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  const hh = String(d.getHours()).padStart(2, '0');
  const mi = String(d.getMinutes()).padStart(2, '0');
  if (g === 'month') return `${y}-${mo}-01`;
  if (g === 'day' || g === 'week') return `${y}-${mo}-${dd}`;
  return `${y}-${mo}-${dd}T${hh}:${mi}:00`;
};

// X-axis day, week, and month keys omit a time zone. Parse those date-only
// labels as local calendar dates so a clicked bucket round-trips to the same
// instant that created it instead of UTC midnight.
const bucketDate = (key: string): Date => {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(key);
  if (match) return new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
  return new Date(key);
};

export { advanceBucket, alignBucketStart, bucketDate, bucketKey, formatTrendTick, trendTickInterval };
export type { TrendGranularity, TrendTickInterval };
