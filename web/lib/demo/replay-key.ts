// The lookup key of a recorded API response in the static demo. It has to equal
// recordKey in cmd/demo-seed/record.go byte for byte; both are tested against
// cmd/demo-seed/testdata/replay-keys.json.

// Query parameters the dashboard fills from its clock. They are keyed as whole
// minutes from the recording moment, so a page whose clock is frozen there asks
// for the same key it was recorded under.
const CLOCK_PARAMS = new Set(['since', 'until', 'from', 'to', 'headline_since', 'headline_until']);

// RFC 3339 with seconds and a zone: what Go's time.Parse(time.RFC3339Nano) accepts.
// A looser match would turn values Go keeps absolute into relative ones.
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

interface ReplayKey {
  key: string;
  // template is the key with every clock value replaced by "@"; offsets are those
  // values in minutes, in template order. Together they let a request whose clock
  // values are a little off -- a week boundary in another time zone -- find the
  // nearest recording of the same request.
  template: string;
  offsets: number[];
}

// Go's math.Round rounds halves away from zero; Math.round rounds them up.
const roundHalfAway = (x: number): number => Math.sign(x) * Math.round(Math.abs(x));

const relativeMinutes = (value: string, now: number): number | null => {
  if (!RFC3339.test(value)) return null;
  const t = Date.parse(value);
  return Number.isNaN(t) ? null : roundHalfAway((t - now) / 60000);
};

const replayKey = (method: string, path: string, query: URLSearchParams, now: number): ReplayKey => {
  const names = Array.from(new Set(query.keys()))
    .filter((name) => name !== 'tz' && (query.get(name) ?? '') !== '')
    .sort();
  const keyParts: string[] = [];
  const templateParts: string[] = [];
  const offsets: number[] = [];
  for (const name of names) {
    const value = query.get(name) ?? '';
    const minutes = CLOCK_PARAMS.has(name) ? relativeMinutes(value, now) : null;
    if (minutes === null) {
      keyParts.push(`${name}=${value}`);
      templateParts.push(`${name}=${value}`);
      continue;
    }
    keyParts.push(`${name}=@${minutes}m`);
    templateParts.push(`${name}=@`);
    offsets.push(minutes);
  }
  const head = `${method.toUpperCase()} ${path}?`;
  return { key: head + keyParts.join('&'), template: head + templateParts.join('&'), offsets };
};

export { replayKey };
export type { ReplayKey };
