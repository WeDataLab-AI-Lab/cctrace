// Answers the trend and cost endpoints for windows nobody recorded -- a step
// back, a zoom, a filter combination -- from the rows of visible_events the
// recorder exported (facts.json), grouped the way the server groups them:
//
//   timeseries-by-user / -by-model  internal/store/postgres_stats_user.go, _model.go
//   cost/by-user / by-model         internal/store/postgres_cost.go
//   stats/latest-activity           internal/store/postgres_stats.go
//   quota-samples                   internal/store/postgres_quota_samples.go
//
// The recorded responses are what this is checked against (compute.check.ts).

interface Facts {
  strings: string[];
  ts: number[]; // microseconds since the epoch
  model: number[];
  user_id: number[];
  profile_email: number[];
  login_email: number[];
  user_team: number[];
  agent: number[];
  billing_provider: number[];
  cost_usd: number[];
  input_tokens: number[];
  output_tokens: number[];
}

type StringColumn = 'model' | 'user_id' | 'profile_email' | 'login_email' | 'user_team' | 'agent' | 'billing_provider';

interface QuotaSample {
  billing_provider: string;
  account_id: string;
  window_key: string;
  sampled_at: string;
  attribution?: string;
  [field: string]: unknown;
}

interface Filter {
  since?: number; // microseconds
  until?: number;
  profileEmail: string;
  loginEmail: string;
  userId: string;
  agent: string;
  category: string;
  model: string;
}

const str = (facts: Facts, column: StringColumn, i: number): string | null => {
  const index = facts[column][i];
  return index < 0 ? null : facts.strings[index];
};

const micros = (value: string | null): number | undefined => {
  if (!value) return undefined;
  const ms = Date.parse(value);
  return Number.isNaN(ms) ? undefined : ms * 1000;
};

// appendAgentScope: "other" is everything but the two agents and the report runs.
const agentMatches = (agent: string | null, scope: string): boolean => {
  if (scope === '') return true;
  if (scope === 'other') return agent !== null && agent !== 'claude' && agent !== 'codex' && agent !== 'weekly';
  return agent === scope;
};

const categoryMatches = (billing: string | null, category: string): boolean => {
  switch (category) {
    case 'anthropic':
      return billing === 'anthropic';
    case 'codex':
      return billing === 'openai';
    case 'compatible':
      return billing !== null && billing !== 'anthropic' && billing !== 'openai';
    default:
      return true;
  }
};

const rowMatches = (facts: Facts, f: Filter, i: number): boolean => {
  const ts = facts.ts[i];
  if (f.since !== undefined && ts < f.since) return false;
  if (f.until !== undefined && ts >= f.until) return false;
  if (f.profileEmail && str(facts, 'profile_email', i) !== f.profileEmail) return false;
  if (f.loginEmail && str(facts, 'login_email', i) !== f.loginEmail) return false;
  if (f.userId && str(facts, 'user_id', i) !== f.userId) return false;
  if (!agentMatches(str(facts, 'agent', i), f.agent)) return false;
  if (!categoryMatches(str(facts, 'billing_provider', i), f.category)) return false;
  if (f.model) {
    const model = str(facts, 'model', i);
    if (model === null || !model.includes(f.model)) return false;
  }
  return true;
};

// The facts are exported in time order, so a window is a slice found by binary
// search rather than a scan of every row.
const firstAtOrAfter = (ts: number[], value: number): number => {
  let lo = 0;
  let hi = ts.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (ts[mid] < value) lo = mid + 1;
    else hi = mid;
  }
  return lo;
};

const rowsIn = (facts: Facts, f: Filter): [number, number] => [
  f.since === undefined ? 0 : firstAtOrAfter(facts.ts, f.since),
  f.until === undefined ? facts.ts.length : firstAtOrAfter(facts.ts, f.until),
];

// The time zone's offset, looked up once per UTC hour.
const offsets = new Map<string, Map<number, number>>();
const formatters = new Map<string, Intl.DateTimeFormat>();

const offsetMinutes = (tz: string, ms: number): number => {
  const hour = Math.floor(ms / 3_600_000);
  const byHour = offsets.get(tz) ?? new Map<number, number>();
  offsets.set(tz, byHour);
  const known = byHour.get(hour);
  if (known !== undefined) return known;
  const fmt = formatters.get(tz) ?? new Intl.DateTimeFormat('en-US', {
    timeZone: tz, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
  });
  formatters.set(tz, fmt);
  const parts = Object.fromEntries(fmt.formatToParts(new Date(hour * 3_600_000)).map((p) => [p.type, Number(p.value)]));
  const local = Date.UTC(parts.year, parts.month - 1, parts.day, parts.hour, parts.minute, parts.second);
  const offset = Math.round((local - hour * 3_600_000) / 60_000);
  byHour.set(hour, offset);
  return offset;
};

const pad = (n: number): string => String(n).padStart(2, '0');
const ymd = (d: Date): string => `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`;

// to_char(date_trunc(granularity, ts AT TIME ZONE tz), ...)
const bucketLabel = (us: number, granularity: string, tz: string): string => {
  const ms = Math.floor(us / 1000);
  const local = new Date(ms + offsetMinutes(tz, ms) * 60_000);
  switch (granularity) {
    case 'minute':
      return `${ymd(local)}T${pad(local.getUTCHours())}:${pad(local.getUTCMinutes())}:00`;
    case 'hour':
      return `${ymd(local)}T${pad(local.getUTCHours())}:00:00`;
    case 'week': {
      const back = (local.getUTCDay() + 6) % 7;
      return ymd(new Date(Date.UTC(local.getUTCFullYear(), local.getUTCMonth(), local.getUTCDate() - back)));
    }
    case 'month':
      return `${local.getUTCFullYear()}-${pad(local.getUTCMonth() + 1)}-01`;
    default:
      return ymd(local);
  }
};

const GRANULARITIES = new Set(['minute', 'hour', 'day', 'week', 'month']);

interface Sums {
  cost: number;
  input: number;
  output: number;
  count: number;
  logins: Set<string>;
}

const accumulate = (groups: Map<string, Sums>, key: string, facts: Facts, i: number) => {
  const sums = groups.get(key) ?? { cost: 0, input: 0, output: 0, count: 0, logins: new Set<string>() };
  sums.cost += facts.cost_usd[i];
  sums.input += facts.input_tokens[i];
  sums.output += facts.output_tokens[i];
  sums.count += 1;
  const login = str(facts, 'login_email', i);
  if (login) sums.logins.add(login);
  groups.set(key, sums);
};

const SEP = '\u0000';

const filterOf = (params: URLSearchParams, model = true): Filter => ({
  since: micros(params.get('since')),
  until: micros(params.get('until')),
  profileEmail: params.get('profile_email') ?? '',
  loginEmail: params.get('login_email') ?? '',
  userId: params.get('user_id') ?? '',
  agent: params.get('agent') ?? '',
  category: params.get('model_category') ?? '',
  model: model ? params.get('model') ?? '' : '',
});

const timeseries = (facts: Facts, params: URLSearchParams, by: 'user' | 'model') => {
  const requested = params.get('granularity') ?? '';
  const granularity = GRANULARITIES.has(requested) ? requested : 'day';
  const tz = params.get('tz') || 'UTC';
  const f = filterOf(params, by === 'user');
  const groups = new Map<string, Sums>();
  const [lo, hi] = rowsIn(facts, f);
  for (let i = lo; i < hi; i += 1) {
    if (!rowMatches(facts, f, i)) continue;
    const label = bucketLabel(facts.ts[i], granularity, tz);
    const key = by === 'user'
      ? [label, str(facts, 'agent', i) === 'weekly' ? 'weekly' : str(facts, 'user_id', i) ?? '', str(facts, 'profile_email', i) ?? ''].join(SEP)
      : [label, str(facts, 'model', i) ?? ''].join(SEP);
    accumulate(groups, key, facts, i);
  }
  const rows = [...groups.entries()].map(([key, s]) => {
    const parts = key.split(SEP);
    const sums = { cost_usd: s.cost, input_tokens: s.input, output_tokens: s.output, event_count: s.count };
    return by === 'user'
      ? { date: parts[0], user_id: parts[1], profile_email: parts[2], ...sums }
      : { date: parts[0], model: parts[1], ...sums };
  });
  return rows.sort((a, b) => {
    if (a.date !== b.date) return a.date < b.date ? -1 : 1;
    const x = 'user_id' in a ? `${a.user_id}${SEP}${a.profile_email}` : a.model;
    const y = 'user_id' in b ? `${b.user_id}${SEP}${b.profile_email}` : b.model;
    return x < y ? -1 : x > y ? 1 : 0;
  });
};

// parseTimeRange: a missing bound is the last seven days up to now.
const costWindow = (params: URLSearchParams, now: number): Filter => ({
  ...filterOf(params, false),
  agent: '',
  category: '',
  since: micros(params.get('since')) ?? (now - 7 * 86_400_000) * 1000,
  until: micros(params.get('until')) ?? now * 1000,
});

const costByUser = (facts: Facts, params: URLSearchParams, now: number) => {
  const f = costWindow(params, now);
  const groups = new Map<string, Sums>();
  const [lo, hi] = rowsIn(facts, f);
  for (let i = lo; i < hi; i += 1) {
    const model = str(facts, 'model', i);
    if (!model || !rowMatches(facts, f, i)) continue;
    const userKey = str(facts, 'agent', i) === 'weekly' ? 'weekly' : str(facts, 'user_id', i) ?? '';
    const key = [str(facts, 'profile_email', i) ?? '', userKey, str(facts, 'user_team', i) ?? '', model,
      str(facts, 'agent', i) ?? 'claude', str(facts, 'billing_provider', i) ?? 'anthropic'].join(SEP);
    accumulate(groups, key, facts, i);
  }
  return [...groups.entries()]
    .map(([key, s]) => {
      const [profile, user, team, model, agent, billing] = key.split(SEP);
      return {
        profile_email: profile,
        ...(user ? { user_id: user } : {}),
        ...(s.logins.size ? { login_emails: [...s.logins].sort() } : {}),
        user_team: team, model, agent, billing_provider: billing,
        total_cost: s.cost, total_input_tokens: s.input, total_output_tokens: s.output, request_count: s.count,
      };
    })
    .sort((a, b) => b.total_cost - a.total_cost);
};

const costByModel = (facts: Facts, params: URLSearchParams, now: number) => {
  const f = costWindow(params, now);
  const groups = new Map<string, Sums>();
  const [lo, hi] = rowsIn(facts, f);
  for (let i = lo; i < hi; i += 1) {
    const model = str(facts, 'model', i);
    if (!model || !rowMatches(facts, f, i)) continue;
    const key = [model, str(facts, 'profile_email', i) ?? '', str(facts, 'user_team', i) ?? '',
      str(facts, 'agent', i) ?? 'claude', str(facts, 'billing_provider', i) ?? 'anthropic'].join(SEP);
    accumulate(groups, key, facts, i);
  }
  return [...groups.entries()]
    .map(([key, s]) => {
      const [model, profile, team, agent, billing] = key.split(SEP);
      return {
        model, ...(profile ? { profile_email: profile } : {}), ...(team ? { user_team: team } : {}),
        agent, billing_provider: billing,
        total_cost: s.cost, input_tokens: s.input, output_tokens: s.output, request_count: s.count,
      };
    })
    .sort((a, b) => b.total_cost - a.total_cost);
};

// time.Time's RFC3339Nano in UTC: the fraction loses its trailing zeros.
const goTime = (us: number): string => {
  const ms = Math.floor(us / 1000);
  const fraction = String(((us % 1_000_000) + 1_000_000) % 1_000_000).padStart(6, '0').replace(/0+$/, '');
  return `${new Date(ms).toISOString().slice(0, 19)}${fraction ? `.${fraction}` : ''}Z`;
};

const latestActivity = (facts: Facts, params: URLSearchParams) => {
  const f = { ...filterOf(params), since: undefined, until: undefined };
  let latest = -Infinity;
  for (let i = 0; i < facts.ts.length; i += 1) {
    if (facts.ts[i] > latest && rowMatches(facts, f, i)) latest = facts.ts[i];
  }
  return Number.isFinite(latest) ? { has_data: true, latest_ts: goTime(latest) } : { has_data: false };
};

// ListQuotaSamples: from and to are inclusive; with both, each series keeps the
// last reading of every (to - from) / 1000 seconds.
const TARGET_POINTS = 1000;

const quotaSamples = (all: QuotaSample[], params: URLSearchParams) => {
  const from = micros(params.get('from'));
  const to = micros(params.get('to'));
  const at = (s: QuotaSample) => Date.parse(s.sampled_at) * 1000;
  const inRange = all.filter((s) => (from === undefined || at(s) >= from) && (to === undefined || at(s) <= to));
  const width = from !== undefined && to !== undefined && to > from ? Math.floor(Math.trunc((to - from) / 1e6) / TARGET_POINTS) : 0;
  if (width <= 0) return inRange;
  const kept = new Map<string, { last: QuotaSample; inferred: boolean }>();
  for (const s of inRange) {
    const bucket = Math.floor(at(s) / 1e6 / width) * width;
    const key = [s.billing_provider, s.account_id, s.window_key, bucket].join(SEP);
    const prior = kept.get(key);
    const inferred = (prior?.inferred ?? false) || s.attribution === 'inferred';
    if (!prior || at(s) > at(prior.last)) kept.set(key, { last: s, inferred });
    else kept.set(key, { last: prior.last, inferred });
  }
  return [...kept.values()]
    .map(({ last, inferred }) => (inferred ? { ...last, attribution: 'inferred' } : last))
    .sort((a, b) => at(a) - at(b) || a.billing_provider.localeCompare(b.billing_provider)
      || a.account_id.localeCompare(b.account_id) || a.window_key.localeCompare(b.window_key));
};

const COMPUTED = new Set([
  '/api/stats/timeseries-by-user', '/api/stats/timeseries-by-model', '/api/cost/by-user', '/api/cost/by-model',
  '/api/stats/latest-activity', '/api/quota-samples',
]);

// computable says whether path is answered from the facts. A project filter
// is not: the facts carry no session-to-project link.
const computable = (path: string, params: URLSearchParams): boolean =>
  COMPUTED.has(path) && !params.has('project_hash') && !params.has('project_hashes');

const compute = (path: string, params: URLSearchParams, facts: Facts, quota: QuotaSample[], now: number): unknown => {
  switch (path) {
    case '/api/stats/timeseries-by-user':
      return timeseries(facts, params, 'user');
    case '/api/stats/timeseries-by-model':
      return timeseries(facts, params, 'model');
    case '/api/cost/by-user':
      return costByUser(facts, params, now);
    case '/api/cost/by-model':
      return costByModel(facts, params, now);
    case '/api/stats/latest-activity':
      return latestActivity(facts, params);
    default:
      return quotaSamples(quota, params);
  }
};

export { compute, computable };
export type { Facts, QuotaSample };
