/**
 * The two formatters below are asymmetric on purpose. `cctrace_version` has a
 * confirmed upper bound: the release that introduced the column
 * (`introducedIn`, from `/api/version`'s `session_record_version_since`). A
 * blank value there is not "unknown" — it provably predates that release, so
 * it is safe (and more informative) to say "< vX".
 *
 * `claude_version` has no such bound: it is read from `raw.version` /
 * `otel_events.service_version`, both of which cover history back further
 * than the cctrace column does. A blank value there really is "unknown" —
 * stamping an upper bound on it would assert something we cannot confirm.
 */
function formatCctraceVersion(value: string | undefined, introducedIn: string): string {
  if (value) return value.startsWith('v') ? value : `v${value}`;
  return `${introducedIn} 미만`;
}

function formatClaudeCodeVersion(value: string | undefined): string {
  return value || '—';
}

export { formatCctraceVersion, formatClaudeCodeVersion };
