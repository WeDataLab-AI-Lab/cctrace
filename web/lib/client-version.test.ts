import { describe, expect, it } from 'vitest';
import {
  classifyClientVersion,
  compareClientVersions,
  parseClientVersion,
  sortClientsByLag,
  summarizeClientVersions,
} from './client-version';

describe('parseClientVersion', () => {
  it('parses a plain semver tag', () => {
    expect(parseClientVersion('v0.7.6')).toEqual({ major: 0, minor: 7, patch: 6, commitsAhead: 0, build: '' });
  });

  // `build` must carry the sha, not just the commit count: it is the only thing
  // that separates two branches sitting the same distance past one tag. Drop the
  // sha from the capture and this fails.
  it('parses a git-describe string with commits-ahead', () => {
    expect(parseClientVersion('v0.6.5-111-gff07e8a')).toEqual({
      major: 0,
      minor: 6,
      patch: 5,
      commitsAhead: 111,
      build: '-111-gff07e8a',
    });
  });

  it('gives two same-distance builds of one tag different build identities', () => {
    expect(parseClientVersion('v0.7.18-11-g83de7e8')!.build).not.toBe(parseClientVersion('v0.7.18-11-gabcdef0')!.build);
  });

  it('parses a dirty-tree build, which is what the server reports today', () => {
    // `git describe --tags --always --dirty` appends -dirty, and deploy/push.sh
    // stamps its output verbatim. Rejecting it made the server version itself
    // unparseable, which fails every client to 'unknown' — the Clients tab then
    // reports nothing about anyone. The suffix carries no ordering, so it is
    // dropped, matching what the client does in cmd/cctrace/update.go.
    expect(parseClientVersion('v0.7.9-dirty')).toEqual({
      major: 0,
      minor: 7,
      patch: 9,
      commitsAhead: 0,
      build: '-dirty',
    });
    expect(parseClientVersion('v0.7.10-152-gbb86a6d-dirty')).toEqual({
      major: 0,
      minor: 7,
      patch: 10,
      commitsAhead: 152,
      build: '-152-gbb86a6d-dirty',
    });
  });

  it('returns null for unparseable or missing input', () => {
    expect(parseClientVersion('not-a-version')).toBeNull();
    // The shape dev used to report before it moved to `git describe`: no semver
    // in it at all, so it stays unknown rather than being guessed at.
    expect(parseClientVersion('dev-0f95f39')).toBeNull();
    expect(parseClientVersion(undefined)).toBeNull();
    expect(parseClientVersion(null)).toBeNull();
    expect(parseClientVersion('')).toBeNull();
  });
});

describe('compareClientVersions', () => {
  it('orders a git-describe string above its base tag and below the next release', () => {
    const base = parseClientVersion('v0.6.5')!;
    const described = parseClientVersion('v0.6.5-111-gff07e8a')!;
    const next = parseClientVersion('v0.7.4')!;
    expect(compareClientVersions(described, base)).toBeGreaterThan(0);
    expect(compareClientVersions(described, next)).toBeLessThan(0);
  });
});

describe('classifyClientVersion', () => {
  it('classifies current/behind/ahead against a known server version', () => {
    expect(classifyClientVersion('v0.7.7', 'v0.7.7')).toBe('current');
    expect(classifyClientVersion('v0.7.4', 'v0.7.7')).toBe('behind');
    expect(classifyClientVersion('v0.7.8', 'v0.7.7')).toBe('ahead');
  });

  // The reported bug: the tab said "outdated" while the client sat there not
  // updating. semverGT in cmd/cctrace/update.go reduces both of these to
  // [0,7,18] and returns false, so no update is ever offered — "outdated" was
  // asking an admin to chase something that cannot move. Nothing within one base
  // tag may be 'behind', in any direction.
  it('never calls a different build of the same tag behind', () => {
    expect(classifyClientVersion('v0.7.18-11-g83de7e8', 'v0.7.18-86-g694e3c4')).toBe('build-differs');
    expect(classifyClientVersion('v0.7.18', 'v0.7.18-86-g694e3c4')).toBe('build-differs');
    expect(classifyClientVersion('v0.7.18-86-g694e3c4', 'v0.7.18-86-g694e3c4')).toBe('current');
  });

  // Same tag, same distance past it, different sha: two branches. These strings
  // are rendered verbatim by formatCctraceVersion, so an admin sees them differ;
  // a 'current' badge next to that is the exact confusion 'build-differs' was
  // added to prevent. Compare builds on commitsAhead alone and this fails.
  it('does not call two branches at the same distance past a tag current', () => {
    expect(classifyClientVersion('v0.7.18-11-g83de7e8', 'v0.7.18-11-gabcdef0')).toBe('build-differs');
  });

  // A dev box running newer code than the server it reports to is routine, and
  // 'ahead' is the state that says so. Collapse every same-tag mismatch into
  // 'build-differs' and this fails.
  it('keeps ahead for a newer build of the same tag', () => {
    expect(classifyClientVersion('v0.7.18-999-gdeadbee', 'v0.7.18-11-g83de7e8')).toBe('ahead');
    expect(classifyClientVersion('v0.7.18-86-g694e3c4', 'v0.7.18')).toBe('ahead');
  });

  // -dirty is part of the build identity, not noise to strip: a dirty tree is a
  // different build from the clean tag it sits on. Strip it and the second
  // assertion returns 'current'. It is still kept out of the equality check's
  // way when both sides report the same string — the first assertion — because
  // calling two identical on-screen strings "different builds" is a visible lie.
  it('treats -dirty as part of the build identity', () => {
    expect(classifyClientVersion('v0.7.9-dirty', 'v0.7.9-dirty')).toBe('current');
    expect(classifyClientVersion('v0.7.9', 'v0.7.9-dirty')).toBe('build-differs');
  });

  // A real lag is still a real lag: different base tag, so the client's own
  // updater would move it. This is what keeps the fix from degenerating into the
  // lazy version of itself — "any build suffix anywhere means build-differs" —
  // under which both of the first two assertions fail.
  it('still classifies a lower base tag as behind regardless of build suffixes', () => {
    expect(classifyClientVersion('v0.7.18', 'v0.7.26')).toBe('behind');
    expect(classifyClientVersion('v0.7.18-999-gdeadbee', 'v0.7.26')).toBe('behind');
    expect(classifyClientVersion('v0.7.26', 'v0.7.18-999-gdeadbee')).toBe('ahead');
  });

  // Fail-closed: any parse failure on either side must yield 'unknown', never
  // a colored guess.
  it('classifies as unknown when server version is missing or unparseable', () => {
    expect(classifyClientVersion('v0.7.7', undefined)).toBe('unknown');
    expect(classifyClientVersion('v0.7.7', 'not-a-version')).toBe('unknown');
  });

  it('classifies as unknown when client version is missing or unparseable', () => {
    expect(classifyClientVersion(undefined, 'v0.7.7')).toBe('unknown');
    expect(classifyClientVersion('not-a-version', 'v0.7.7')).toBe('unknown');
  });

  // Empty string is a confirmed floor (no client_versions row — the client
  // never sent the version header at all), distinct from an unparseable
  // string, which merely means "can't tell".
  it('classifies an empty client version as unreported, not unknown', () => {
    expect(classifyClientVersion('', 'v0.7.7')).toBe('unreported');
    expect(classifyClientVersion('', undefined)).toBe('unreported');
  });
});

describe('sortClientsByLag', () => {
  // Measured distribution from the plan: v0.7.7(0) v0.7.6 v0.7.4 v0.6.5-111-gff07e8a v0.6.4 unreported (empty).
  const rows = [
    { client_version: 'v0.7.6', profile_email: 'a@x.com', last_seen: '2026-08-10T00:00:00Z' },
    { client_version: 'v0.7.4', profile_email: 'b@x.com', last_seen: '2026-08-09T00:00:00Z' },
    { client_version: 'v0.6.5-111-gff07e8a', profile_email: 'c@x.com', last_seen: '2026-08-08T00:00:00Z' },
    { client_version: 'v0.6.4', profile_email: 'd@x.com', last_seen: '2026-08-07T00:00:00Z' },
    { client_version: '', profile_email: 'e@x.com', last_seen: '2026-08-06T00:00:00Z' },
  ];

  it('orders unreported first (confirmed floor, most urgent), then behind, then current/ahead', () => {
    const sorted = sortClientsByLag(rows, 'v0.7.7', (r) => r.last_seen);
    expect(sorted.map((r) => r.profile_email)).toEqual(['e@x.com', 'd@x.com', 'c@x.com', 'b@x.com', 'a@x.com']);
  });

  it('is deterministic across repeated calls with the same input (no shuffling on repoll)', () => {
    const first = sortClientsByLag(rows, 'v0.7.7', (r) => r.last_seen).map((r) => r.profile_email);
    const second = sortClientsByLag(rows, 'v0.7.7', (r) => r.last_seen).map((r) => r.profile_email);
    expect(second).toEqual(first);
  });

  it('marks every row unknown or unreported (no coloring) when the server version cannot be parsed', () => {
    const sorted = sortClientsByLag(rows, undefined, (r) => r.last_seen);
    expect(
      sorted.every((r) => {
        const status = classifyClientVersion(r.client_version, undefined);
        return status === 'unknown' || status === 'unreported';
      }),
    ).toBe(true);
  });

  // commitsAhead no longer decides status, but it must still order rows within a
  // bucket: it is what tells an admin which of two same-tag builds is older. The
  // emails are chosen so the alphabetical last-resort tie-break would produce the
  // opposite order — drop the commitsAhead tie-break from compareClientVersions
  // and this fails rather than passing by coincidence.
  it('orders same-tag builds by commits-ahead within the build-differs bucket', () => {
    const sameTag = [
      { client_version: 'v0.7.18-25-gbbbbbbb', profile_email: 'a-newer@x.com', last_seen: '2026-08-01T00:00:00Z' },
      { client_version: 'v0.7.18-11-g83de7e8', profile_email: 'z-older@x.com', last_seen: '2026-08-01T00:00:00Z' },
    ];
    const sorted = sortClientsByLag(sameTag, 'v0.7.18-40-gaaaaaaa', (r) => r.last_seen);
    expect(sorted.map((r) => r.profile_email)).toEqual(['z-older@x.com', 'a-newer@x.com']);
  });

  // Both rows are the same distance past the tag, so version ordering is a tie
  // and only the status rank can separate them — which is the point: an admin
  // scanning down the tab should meet the row that is NOT on the server build
  // before the row that is. Give 'build-differs' the same rank as 'current' and
  // the email tie-break takes over and puts a@x.com first.
  it('ranks a same-tag other build above the row that matches the server exactly', () => {
    const rowsAtSameDistance = [
      { client_version: 'v0.7.18-40-gaaaaaaa', profile_email: 'a@x.com', last_seen: '2026-08-01T00:00:00Z' },
      { client_version: 'v0.7.18-40-gbbbbbbb', profile_email: 'z@x.com', last_seen: '2026-08-01T00:00:00Z' },
    ];
    const sorted = sortClientsByLag(rowsAtSameDistance, 'v0.7.18-40-gaaaaaaa', (r) => r.last_seen);
    expect(sorted.map((r) => r.profile_email)).toEqual(['z@x.com', 'a@x.com']);
  });

  // The summary counts a newer same-tag build as not-on-the-server-build, so the
  // sort must not bury it under the green 'current' rows: the two decisions have
  // to point the same way. Rank 'ahead' below 'current' and this fails — which is
  // what happened once 'ahead' took over the newer-build case from
  // 'build-differs', the only rank the previous test pins.
  it('ranks a newer build of the server tag above the row that matches the server exactly', () => {
    const rowsWithAhead = [
      { client_version: 'v0.7.18-86-g694e3c4', profile_email: 'a-current@x.com', last_seen: '2026-08-01T00:00:00Z' },
      { client_version: 'v0.7.18-999-gdeadbee', profile_email: 'z-ahead@x.com', last_seen: '2026-08-01T00:00:00Z' },
    ];
    const sorted = sortClientsByLag(rowsWithAhead, 'v0.7.18-86-g694e3c4', (r) => r.last_seen);
    expect(sorted.map((r) => r.profile_email)).toEqual(['z-ahead@x.com', 'a-current@x.com']);
  });

  // 'build-differs' and 'ahead' share one rank, so an older and a newer build of
  // the server tag keep version order relative to each other while both sit above
  // 'current'. Split the rank and the two non-server builds get separated by
  // status instead of by version.
  it('keeps older and newer builds of the server tag in version order above current', () => {
    const mixed = [
      { client_version: 'v0.7.18-86-g694e3c4', profile_email: 'current@x.com', last_seen: '2026-08-01T00:00:00Z' },
      { client_version: 'v0.7.18-999-gdeadbee', profile_email: 'newer@x.com', last_seen: '2026-08-01T00:00:00Z' },
      { client_version: 'v0.7.18-11-g83de7e8', profile_email: 'older@x.com', last_seen: '2026-08-01T00:00:00Z' },
    ];
    const sorted = sortClientsByLag(mixed, 'v0.7.18-86-g694e3c4', (r) => r.last_seen);
    expect(sorted.map((r) => r.profile_email)).toEqual(['older@x.com', 'newer@x.com', 'current@x.com']);
  });

  it('tie-breaks equal status/version/last_seen by profile_email', () => {
    const tied = [
      { client_version: 'v0.7.4', profile_email: 'zed@x.com', last_seen: '2026-08-01T00:00:00Z' },
      { client_version: 'v0.7.4', profile_email: 'able@x.com', last_seen: '2026-08-01T00:00:00Z' },
    ];
    const sorted = sortClientsByLag(tied, 'v0.7.7', (r) => r.last_seen);
    expect(sorted.map((r) => r.profile_email)).toEqual(['able@x.com', 'zed@x.com']);
  });
});

describe('summarizeClientVersions', () => {
  it('does not say "0 behind" when every client is unknown because the server version is unparseable', () => {
    const rows = [
      { client_version: 'v0.5.9', profile_email: 'a@x.com' },
      { client_version: 'v0.6.0', profile_email: 'b@x.com' },
      { client_version: 'v0.6.0', profile_email: 'c@x.com' },
    ];
    const summary = summarizeClientVersions(rows, 'dev-fdeed05');
    expect(summary.text).not.toMatch(/0 of 3 behind/);
    expect(summary.text.toLowerCase()).toContain('unknown');
  });

  it('reports both behind and unknown counts when mixed', () => {
    const rows = [
      { client_version: 'v0.7.4', profile_email: 'a@x.com' }, // behind
      { client_version: 'v0.7.7', profile_email: 'b@x.com' }, // current
      { client_version: 'not-a-version', profile_email: 'c@x.com' }, // unknown
    ];
    const summary = summarizeClientVersions(rows, 'v0.7.7');
    expect(summary.behindCount).toBe(1);
    expect(summary.unknownCount).toBe(1);
    expect(summary.text).toMatch(/1 of 3 behind/);
    expect(summary.text.toLowerCase()).toContain('unknown');
  });

  it('reports plain behind count with no unknown mention when there are no unknowns', () => {
    const rows = [
      { client_version: 'v0.7.4', profile_email: 'a@x.com' },
      { client_version: 'v0.7.7', profile_email: 'b@x.com' },
    ];
    const summary = summarizeClientVersions(rows, 'v0.7.7');
    expect(summary.unknownCount).toBe(0);
    expect(summary.text).toBe('1 of 2 behind v0.7.7');
  });

  it('does not count a different build of the server tag as behind', () => {
    const rows = [
      { client_version: 'v0.7.18-11-g83de7e8', profile_email: 'a@x.com' },
      { client_version: 'v0.7.18-86-g694e3c4', profile_email: 'b@x.com' },
    ];
    const summary = summarizeClientVersions(rows, 'v0.7.18-86-g694e3c4');
    expect(summary.behindCount).toBe(0);
    expect(summary.text).toBe('0 of 2 behind v0.7.18-86-g694e3c4, 1 other build');
  });

  // The develop-branch steady state: every dev box carries its own build of the
  // current tag, so behindCount is legitimately 0. Report only that and the line
  // reads "all twelve up to date" — the opposite of the truth, which is that
  // none of them is on the server build and none will get there by itself.
  // Stop counting 'build-differs'/'ahead' and this fails.
  it('surfaces same-tag other builds instead of letting "0 behind" imply everyone is current', () => {
    const rows = Array.from({ length: 12 }, (_, i) => ({
      client_version: `v0.7.18-${i + 1}-g83de7e${i.toString(16)}`,
      profile_email: `dev${i}@x.com`,
    }));
    const summary = summarizeClientVersions(rows, 'v0.7.18-86-g694e3c4');
    expect(summary.behindCount).toBe(0);
    expect(summary.otherBuildCount).toBe(12);
    expect(summary.text).toBe('0 of 12 behind v0.7.18-86-g694e3c4, 12 other builds');
  });

  // The invariant the whole summary rests on, stated as what is NOT counted:
  // every row the line stays silent about is on exactly the server build. A
  // status that escapes every counter — 'ahead' did — breaks it silently,
  // so assert it directly over one row of every status.
  it('counts every row that is not exactly on the server build', () => {
    const rows = [
      { client_version: 'v0.7.4', profile_email: 'behind@x.com' },
      { client_version: 'not-a-version', profile_email: 'unknown@x.com' },
      { client_version: '', profile_email: 'unreported@x.com' },
      { client_version: 'v0.7.18-11-g83de7e8', profile_email: 'otherbuild@x.com' },
      { client_version: 'v0.7.18-999-gdeadbee', profile_email: 'ahead@x.com' },
      { client_version: 'v0.7.18-86-g694e3c4', profile_email: 'current@x.com' },
    ];
    const summary = summarizeClientVersions(rows, 'v0.7.18-86-g694e3c4');
    const counted =
      summary.behindCount +
      summary.unknownCount +
      summary.unreportedCount +
      summary.otherBuildCount +
      summary.aheadCount;
    const onServerBuild = rows.filter(
      (r) => classifyClientVersion(r.client_version, 'v0.7.18-86-g694e3c4') === 'current',
    ).length;
    expect(counted + onServerBuild).toBe(summary.total);
    expect(onServerBuild).toBe(1);
  });

  // The summary and the badges have to name the same thing the same way: this row
  // is badged 'ahead' in the table (STATUS_LABEL in clients-tab.tsx), so the line
  // must say "ahead" too. Fold 'ahead' back into otherBuildCount and the line says
  // "1 other build" while no row in the table carries that badge — and "other
  // build" is doubly wrong here, since v0.7.26 is a later release, not another
  // build of the server's.
  it('reports an ahead row with the same word the badge uses', () => {
    const rows = [
      { client_version: 'v0.7.26', profile_email: 'a@x.com' },
      { client_version: 'v0.7.18-86-g694e3c4', profile_email: 'b@x.com' },
    ];
    const summary = summarizeClientVersions(rows, 'v0.7.18-86-g694e3c4');
    expect(summary.aheadCount).toBe(1);
    expect(summary.otherBuildCount).toBe(0);
    expect(summary.text).toBe('0 of 2 behind v0.7.18-86-g694e3c4, 1 ahead');
  });

  it('handles zero clients', () => {
    const summary = summarizeClientVersions([], 'v0.7.7');
    expect(summary.text).toBe('0 clients');
  });

  it('handles no server version with no clients unknown-suppressed wording', () => {
    const rows = [{ client_version: 'v0.7.4', profile_email: 'a@x.com' }];
    const summary = summarizeClientVersions(rows, undefined);
    expect(summary.unknownCount).toBe(1);
    expect(summary.behindCount).toBe(0);
    expect(summary.text.toLowerCase()).toContain('unknown');
  });

  it('reports unreported accounts separately from behind and unknown', () => {
    const rows = [
      { client_version: 'v0.7.4', profile_email: 'a@x.com' }, // behind
      { client_version: 'v0.7.7', profile_email: 'b@x.com' }, // current
      { client_version: '', profile_email: 'c@x.com' }, // unreported
    ];
    const summary = summarizeClientVersions(rows, 'v0.7.7');
    expect(summary.behindCount).toBe(1);
    expect(summary.unreportedCount).toBe(1);
    expect(summary.unknownCount).toBe(0);
    expect(summary.text).toBe('1 of 3 behind v0.7.7, 1 unreported');
  });
});
