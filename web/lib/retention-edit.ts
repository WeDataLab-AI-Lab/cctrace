import type { RetentionPreview } from './types';

/**
 * A retention change is destructive (needs typed confirmation) if any governed
 * table would have rows become drop-eligible. Increases / 0(permanent) report 0.
 */
export function retentionChangeIsDangerous(preview: RetentionPreview[]): boolean {
  return preview.some((p) => p.rows_to_drop > 0);
}

/** Total drop-eligible rows across the axis's tables. */
export function totalRowsToDrop(preview: RetentionPreview[]): number {
  return preview.reduce((sum, p) => sum + p.rows_to_drop, 0);
}

/**
 * The preview is authoritative ONLY when it reflects the current days value and
 * loaded cleanly. While typing, during the debounce lag (days !== debouncedDays),
 * in-flight, or on a failed fetch, the impact is UNKNOWN and callers must fail
 * closed (do not treat the change as safe).
 */
export function previewIsFresh(opts: {
  valid: boolean;
  daysMatch: boolean;
  isFetching: boolean;
  isError: boolean;
}): boolean {
  return opts.valid && opts.daysMatch && !opts.isFetching && !opts.isError;
}

/**
 * Apply is permitted only with a fresh preview, no in-flight mutation, and — when
 * the change is destructive — an explicit typed confirmation. A non-fresh preview
 * (unknown impact) always blocks Apply, so a destructive change can never slip
 * through as "safe".
 */
export function canApplyRetention(opts: {
  previewFresh: boolean;
  pending: boolean;
  dangerous: boolean;
  confirmedTyped: boolean;
}): boolean {
  return opts.previewFresh && !opts.pending && (!opts.dangerous || opts.confirmedTyped);
}
