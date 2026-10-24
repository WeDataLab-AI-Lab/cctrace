import { answer, indexEntries, routeOf, shardOf } from './replayer';
import type { RecordedEntry, ReplaySource, ShardIndex } from './replayer';
import type { Facts, QuotaSample } from './compute';

// The browser side of the static demo, installed from instrumentation-client.ts
// before the app's own code runs, and only in a demo build.
//
// It works at the network boundary, as a mock server would: the app keeps
// calling its real API client, and window.fetch answers from the recordings.
// The clock is frozen at the recording moment, because every chart asks for
// "the last N hours" from Date.now() and only that moment was recorded.

const DEMO_EVENT = 'cctrace-demo';

interface DemoNotice {
  kind: 'read-only' | 'missing';
  key: string;
}

interface DemoRuntimeOptions {
  basePath: string;
  recordedAt: string;
}

interface Manifest {
  recorded_at: string;
  shards: string[];
}

const json = (status: number, body: unknown, headers: Record<string, string> = {}): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });

const notify = (notice: DemoNotice) => {
  window.dispatchEvent(new CustomEvent<DemoNotice>(DEMO_EVENT, { detail: notice }));
};

// Only "now" changes: new Date() and Date.now() read the recording moment, and
// every other form of new Date(...) passes its arguments through.
const freezeClock = (now: number) => {
  class FrozenDate extends Date {
    constructor(...args: unknown[]) {
      if (args.length === 0) {
        super(now);
      } else {
        super(...(args as [number]));
      }
    }

    static now() {
      return now;
    }
  }
  globalThis.Date = FrozenDate as DateConstructor;
};

const requestOf = (input: RequestInfo | URL, init?: RequestInit): { url: URL; method: string } => {
  const href = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
  const method = init?.method ?? (input instanceof Request ? input.method : 'GET');
  return { url: new URL(href, window.location.href), method };
};

const once = <T>(load: () => Promise<T>): (() => Promise<T>) => {
  let pending: Promise<T> | null = null;
  return () => {
    pending ??= load();
    return pending;
  };
};

// fetchSource reads the recordings next to the page: manifest.json lists one
// file per endpoint under manifest/, each fetched the first time it is needed,
// and the usage facts behind the computed answers.
const fetchSource = (realFetch: typeof fetch, dataRoot: string, now: number): ReplaySource & { body(hash: string): Promise<string> } => {
  const getJSON = <T>(path: string) => realFetch(`${dataRoot}/${path}`).then((res) => res.json() as Promise<T>);
  // Response bodies and the facts are published gzipped (a sixth of the size);
  // Pages serves them as plain files, so the page inflates them itself.
  const getGzipText = (path: string) =>
    realFetch(`${dataRoot}/${path}.gz`).then((res) => {
      if (!res.ok || !res.body) throw new Error(`demo: ${path}.gz: ${res.status}`);
      return new Response(res.body.pipeThrough(new DecompressionStream('gzip'))).text();
    });
  const manifest = once(() => getJSON<Manifest>('manifest.json'));
  const shards = new Map<string, Promise<RecordedEntry[]>>();
  const entries = (name: string) => {
    const known = shards.get(name);
    if (known) return known;
    const loading = getJSON<RecordedEntry[]>(`manifest/${name}`);
    shards.set(name, loading);
    return loading;
  };
  const indexes = new Map<string, Promise<ShardIndex | null>>();
  const bodies = new Map<string, Promise<string>>();
  const body = (hash: string) => {
    const known = bodies.get(hash);
    if (known) return known;
    const loading = getGzipText(`bodies/${hash}.json`);
    bodies.set(hash, loading);
    return loading;
  };
  return {
    now,
    body,
    shard: (name) => {
      const known = indexes.get(name);
      if (known) return known;
      const loading = manifest().then((m) => (m.shards.includes(name) ? entries(name).then(indexEntries) : null));
      indexes.set(name, loading);
      return loading;
    },
    facts: once(() => getGzipText('facts.json').then((text) => JSON.parse(text) as Facts)),
    // Every reading: the recording with the earliest "from" and no "to".
    quota: once(async () => {
      const fromOf = (key: string) => Number(/[?&]from=@(-?\d+)m/.exec(key)?.[1] ?? Infinity);
      const [whole] = (await entries(shardOf('/api/quota-samples')))
        .filter((e) => !/[?&]to=/.test(e.key))
        .sort((a, b) => fromOf(a.key) - fromOf(b.key));
      return whole ? (JSON.parse(await body(whole.body)) as QuotaSample[]) : [];
    }),
  };
};

const installDemoRuntime = ({ basePath, recordedAt }: DemoRuntimeOptions) => {
  const recorded = Date.parse(recordedAt);
  if (Number.isNaN(recorded)) throw new Error(`demo: invalid recording time ${JSON.stringify(recordedAt)}`);
  freezeClock(recorded);

  const realFetch = window.fetch.bind(window);
  const source = fetchSource(realFetch, `${basePath}/demo-data`, recorded);

  const demoFetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const { url, method } = requestOf(input, init);
    const route = routeOf(url, window.location.origin);
    // The dashboard can pair with a cctrace daemon on the visitor's own machine.
    // A public page must not reach for it, so the demo reports it unreachable.
    if (route === 'blocked') throw new TypeError('Failed to fetch');
    if (route === 'network') return realFetch(input, init);

    const result = await answer(source, method, url);
    switch (result.kind) {
      case 'refresh':
        return json(200, {});
      case 'read-only':
        notify({ kind: 'read-only', key: `${method.toUpperCase()} ${url.pathname}` });
        return json(403, { error: 'demo_read_only' });
      case 'missing':
        notify({ kind: 'missing', key: result.key });
        return json(404, { error: 'not recorded in this demo' });
      case 'computed':
        return json(200, result.body);
      case 'recorded':
        return new Response(await source.body(result.entry.body), {
          status: result.entry.status,
          headers: { 'Content-Type': 'application/json', ...result.entry.headers },
        });
    }
  };
  window.fetch = demoFetch;
};

export { DEMO_EVENT, installDemoRuntime };
export type { DemoNotice };
