/**
 * Human-readable byte size for storage panels (B → TB). Non-finite or negative
 * inputs render as '—' so a missing/garbage value never shows a wrong number.
 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(1)} ${units[unit]}`;
}

/** Retention interval label; null/undefined means no policy (kept forever). */
export function formatRetentionDays(days: number | null | undefined): string {
  if (days == null) return 'Unlimited';
  return `${days}d`;
}

/**
 * Compression interval label; null/undefined means no active compression policy
 * — i.e. the table is never compressed. This must NOT reuse the retention label:
 * "Unlimited" would wrongly read as "unlimited compression".
 */
export function formatCompressionDays(days: number | null | undefined): string {
  if (days == null) return 'None';
  return `${days}d`;
}

const formatRelativeTime = (timestamp: string, now = Date.now()): string => {
  const minutes = Math.floor((now - new Date(timestamp).getTime()) / 60_000);
  if (minutes < 1) return 'Just now';
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
};

/**
 * Large counts, shortened for a place where the digits are not the point.
 *
 * The coverage tooltip's fitted constant reads `k=96365426 tokens/%`, and no
 * reader of that panel is comparing the eighth digit -- they are checking the
 * order of magnitude while scanning a column of accounts. Intl does the
 * locale-correct thing with the suffix.
 *
 * Deliberately not folded together with the two private formatters in
 * trend-chart.tsx and overview-helpers.ts: those round differently
 * ((n/1000).toFixed(0) + 'K'), so converging them would silently change labels
 * that are already on screen. That convergence is its own change.
 */
const formatCompactNumber = (n: number): string =>
  new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(n);

export { formatRelativeTime, formatCompactNumber };
