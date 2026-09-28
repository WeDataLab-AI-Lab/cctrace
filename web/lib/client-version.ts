interface ParsedClientVersion {
  major: number;
  minor: number;
  patch: number;
  /** Commits ahead of the base tag in a git-describe string ("v0.6.5-111-gff07e8a" -> 111). 0 for a plain semver tag. */
  commitsAhead: number;
  /**
   * Everything after the base tag, verbatim: "-111-gff07e8a", "-dirty",
   * "-152-gbb86a6d-dirty", or "" for a plain tag. This is the build identity —
   * two versions are the same build only if it matches, which is why the commit
   * sha has to be captured rather than discarded. Without the sha,
   * "v0.7.18-11-g83de7e8" and "v0.7.18-11-gabcdef0" — two different branches,
   * visibly different strings on screen — compare equal.
   *
   * "-dirty" is kept in the identity rather than stripped. A dirty tree is not
   * reproducible, so matching strings are not proof of matching bits; but the
   * dashboard can only compare what the two sides report, and telling an admin
   * that two identical strings are "different builds" is a visible lie, while
   * the reverse error is invisible and costs nothing (neither status asks for
   * action). A dirty build does differ from the clean tag it sits on, and that
   * the identity does catch.
   */
  build: string;
}

type ClientVersionStatus = 'current' | 'build-differs' | 'behind' | 'ahead' | 'unknown' | 'unreported';

interface VersionedRow {
  client_version: string;
  profile_email: string;
}

/**
 * Parses the three shapes deploy/push.sh can stamp, all of them from
 * `git describe --tags --always --dirty`: a plain tag ("v0.7.6"), a build past a
 * tag ("v0.6.5-111-gff07e8a"), and either of those from a dirty tree
 * ("v0.7.9-dirty"). Returns null for anything else — callers must treat that as
 * unknown, not as version 0.0.0.
 *
 * The -dirty suffix carries no ordering and is dropped. That is not cosmetic:
 * rejecting it made the SERVER version unparseable, and an unparseable server
 * version fails every client to 'unknown' — the Clients tab then reports nothing
 * about anyone, which is the state prod has been in.
 *
 * commitsAhead is parsed, but only as ordering information (compareClientVersions,
 * and through it the Clients-tab sort). It deliberately does NOT feed the
 * current/behind/ahead decision: the client's own updater reduces a git-describe
 * string to its base tag (parseSemver in cmd/cctrace/update.go, pinned by
 * TestUpdateDecisionOverRealVersionShapes) and so never updates across builds of
 * one tag. Letting the dashboard call such a client "outdated" would flag work an
 * admin cannot action — the client is not going to move.
 */
function parseClientVersion(raw: string | undefined | null): ParsedClientVersion | null {
  if (!raw) return null;
  const match = /^v?(\d+)\.(\d+)\.(\d+)((?:-(\d+)-g[0-9a-f]+)?(?:-[0-9A-Za-z.]+)?)$/.exec(raw.trim());
  if (!match) return null;
  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
    commitsAhead: match[5] ? Number(match[5]) : 0,
    build: match[4],
  };
}

/** Base tag only (major.minor.patch) — the granularity the client's updater compares at. */
function compareBaseVersions(a: ParsedClientVersion, b: ParsedClientVersion): number {
  if (a.major !== b.major) return a.major - b.major;
  if (a.minor !== b.minor) return a.minor - b.minor;
  return a.patch - b.patch;
}

/**
 * Ordering (not status): base semver first, then commitsAhead as a tie-breaker.
 * This is what makes "v0.6.5-111-gff07e8a" sort above "v0.6.5" (111 commits past
 * it) but below "v0.7.4" (a later base version) — the only reading of a
 * git-describe string that is both correct and explainable. classifyClientVersion
 * intentionally stops at compareBaseVersions; see parseClientVersion.
 */
function compareClientVersions(a: ParsedClientVersion, b: ParsedClientVersion): number {
  const base = compareBaseVersions(a, b);
  if (base !== 0) return base;
  return a.commitsAhead - b.commitsAhead;
}

/**
 * Fail-closed: any parse failure (client or server) yields 'unknown' rather
 * than a guess. An 'unknown' badge is honest; a wrongly colored 'current' or
 * 'behind' badge is not.
 *
 * An empty client_version is not a parse failure — it means the account has
 * never sent the X-Cctrace-Version header, whether because no client_versions
 * row exists or because every sync it made arrived without the header (since
 * #455 a headerless sync writes a row with an empty version, and an empty
 * value never overwrites one already reported). That is worse than "behind":
 * it is a known floor rather than an estimate, so it gets its own 'unreported'
 * status instead of being folded into 'unknown'.
 *
 * 'behind' is decided on the base tag alone, so that a red badge here means the
 * same thing the client's updater means: it will actually move. Within one base
 * tag the client never updates, so nothing inside a tag may return 'behind' —
 * that is the whole point of this issue.
 *
 * Inside one base tag the remaining three states split by build identity:
 *   - identical build  -> 'current'
 *   - more commits past the tag than the server -> 'ahead' (a dev box running
 *     newer code than the server it reports to is common and worth seeing; it is
 *     what 'ahead' has always meant, just now within a tag as well as across
 *     tags). commitsAhead is a count, not a proof of descent — two branches can
 *     both be N commits past the tag — but both 'ahead' and 'build-differs' are
 *     non-actionable, so guessing wrong between them costs nothing.
 *   - anything else (fewer commits, or the same count on a different sha)
 *     -> 'build-differs': same release, different build, no update coming.
 * 'build-differs' exists rather than folding into 'current' because the two
 * version strings on screen visibly differ, and calling that plainly "current"
 * invites a bug report.
 */
function classifyClientVersion(
  clientVersion: string | undefined | null,
  serverVersion: string | undefined | null,
): ClientVersionStatus {
  if (clientVersion === '') return 'unreported';
  const client = parseClientVersion(clientVersion);
  const server = parseClientVersion(serverVersion);
  if (!client || !server) return 'unknown';
  const cmp = compareBaseVersions(client, server);
  if (cmp < 0) return 'behind';
  if (cmp > 0) return 'ahead';
  if (client.build === server.build) return 'current';
  if (client.commitsAhead > server.commitsAhead) return 'ahead';
  return 'build-differs';
}

/**
 * Deterministic order so polling never reshuffles the table while an admin is
 * looking at it: most-behind first, then version ascending, then last_seen
 * descending (via the caller-supplied accessor, since last_seen isn't on
 * every row shape), with profile_email as the final tie-break.
 *
 * 'unreported' ranks above 'behind' because an unknown version cannot be ruled
 * out as the oldest one. It is not a confirmed floor: a build without the
 * version ldflag sends no header either, so the bucket holds both pre-v0.6.1
 * clients and current binaries built without release flags.
 *
 * 'build-differs' and 'ahead' share one rank, below 'unknown' and above
 * 'current': the argument for lifting them out of the green rows is the same for
 * both — nothing to action, but the row is not on the server build and the
 * summary line counts it, so burying it under 'current' would point the sort the
 * opposite way from the summary. Sharing the rank rather than splitting it lets
 * version order (compareClientVersions) decide between them, which is the only
 * distinction an admin can read off the two strings anyway.
 */
function sortClientsByLag<T extends VersionedRow>(
  rows: readonly T[],
  serverVersion: string | undefined | null,
  lastSeenOf: (row: T) => string,
): T[] {
  const rank: Record<ClientVersionStatus, number> = {
    unreported: 0,
    behind: 1,
    unknown: 2,
    'build-differs': 3,
    ahead: 3,
    current: 4,
  };
  return [...rows].sort((a, b) => {
    const statusA = classifyClientVersion(a.client_version, serverVersion);
    const statusB = classifyClientVersion(b.client_version, serverVersion);
    if (rank[statusA] !== rank[statusB]) return rank[statusA] - rank[statusB];

    const parsedA = parseClientVersion(a.client_version);
    const parsedB = parseClientVersion(b.client_version);
    if (parsedA && parsedB) {
      const cmp = compareClientVersions(parsedA, parsedB);
      if (cmp !== 0) return cmp;
    }

    const seenCmp = lastSeenOf(b).localeCompare(lastSeenOf(a));
    if (seenCmp !== 0) return seenCmp;

    return a.profile_email.localeCompare(b.profile_email);
  });
}

interface ClientVersionSummary {
  behindCount: number;
  unknownCount: number;
  unreportedCount: number;
  /**
   * Rows whose badge reads 'other build' — status 'build-differs' only. 'ahead'
   * gets its own counter rather than being folded in here: the summary and the
   * table have to use one vocabulary, and merging them printed "1 other build"
   * for a row the table badges 'ahead', leaving an admin hunting the table for a
   * badge that does not exist. Worse, 'ahead' also covers a whole later release
   * ("v0.7.26" against a v0.7.18 server), which is not "another build of this
   * release" by any reading.
   *
   * Both counters still exist so that the invariant holds: every row the summary
   * stays silent about is on exactly the server build.
   */
  otherBuildCount: number;
  aheadCount: number;
  total: number;
  text: string;
}

/**
 * Builds the Clients-tab summary line. Must never let "N of M behind" imply
 * everyone else is current when some rows are actually unparseable (e.g. the
 * server reports a git-describe tag like "dev-fdeed05") — that reads as "all
 * up to date" when it really means "can't tell". Unknown and unreported
 * counts are always surfaced explicitly alongside the behind count.
 *
 * Same rule for builds that are not the server's. On develop every dev box runs
 * its own "v0.7.18-N-g<sha>", so "0 of 12 behind v0.7.18-86-g694e3c4" on its own
 * reads as "all twelve are up to date" when in fact none of them is on the
 * server build and none of them will ever get there on its own. Not actionable
 * is not the same as not worth saying, and the badges already draw the
 * distinction — hiding it only in the summary was an asymmetry with no reason
 * behind it.
 */
function summarizeClientVersions<T extends VersionedRow>(
  rows: readonly T[],
  serverVersion: string | undefined | null,
): ClientVersionSummary {
  const total = rows.length;
  if (total === 0) {
    return {
      behindCount: 0,
      unknownCount: 0,
      unreportedCount: 0,
      otherBuildCount: 0,
      aheadCount: 0,
      total: 0,
      text: '0 clients',
    };
  }

  let behindCount = 0;
  let unknownCount = 0;
  let unreportedCount = 0;
  let otherBuildCount = 0;
  let aheadCount = 0;
  for (const row of rows) {
    const status = classifyClientVersion(row.client_version, serverVersion);
    if (status === 'behind') behindCount += 1;
    if (status === 'unknown') unknownCount += 1;
    if (status === 'unreported') unreportedCount += 1;
    if (status === 'build-differs') otherBuildCount += 1;
    if (status === 'ahead') aheadCount += 1;
  }
  const counts = { behindCount, unknownCount, unreportedCount, otherBuildCount, aheadCount, total };

  if (!serverVersion || unknownCount + unreportedCount === total) {
    return { ...counts, text: `${unknownCount + unreportedCount} of ${total} unknown (server version unavailable)` };
  }

  const base = `${behindCount} of ${total} behind ${serverVersion}`;
  const extras: string[] = [];
  if (unreportedCount > 0) extras.push(`${unreportedCount} unreported`);
  if (unknownCount > 0) extras.push(`${unknownCount} unknown`);
  // Each extra uses the same word as the badge it stands for (STATUS_LABEL in
  // clients-tab.tsx), so an admin reading "1 ahead" can find that row in the table.
  if (otherBuildCount > 0) extras.push(`${otherBuildCount} other build${otherBuildCount === 1 ? '' : 's'}`);
  if (aheadCount > 0) extras.push(`${aheadCount} ahead`);
  if (extras.length === 0) {
    return { ...counts, text: base };
  }
  return { ...counts, text: `${base}, ${extras.join(', ')}` };
}

export { parseClientVersion, compareClientVersions, classifyClientVersion, sortClientsByLag, summarizeClientVersions };
export type { ParsedClientVersion, ClientVersionStatus, VersionedRow, ClientVersionSummary };
