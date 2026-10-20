/**
 * 캘린더 미리보기의 합성 주를 만든다. 레퍼런스의 한 주(138 세션)만큼 붐비게: 이번 주와 지난주가 각각 150건 안팎이고,
 * 낮과 밤, 겹침, 자정을 넘는 세션, 며칠에 걸쳐 이어 한 세션, 입력이 수백 개인 세션, 요청 없는 세션이 모두 들어 있다.
 *
 * 시각은 서울(KST, UTC+9) 달력으로 짠다 — 미리보기의 기본 시간대가 서울이라 "월요일 9시"가 화면에서도 월요일 9시다.
 * 같은 시드는 언제나 같은 주를 만든다. 합성 데이터라 실제 세션과 무관하다.
 */

import type { AnalysisProject, AnalysisState, AnalysisTitle, CostSource, PreviewState } from '@/lib/session-analysis-types';
import type { CalendarStatus } from '@/lib/session-calendar-types';
import { createRng, type Rng } from './prng';
import {
  BRANCHES,
  CLAUDE_MODELS,
  CODEX_MODELS,
  FOLLOW_UPS,
  KINDS,
  PROFILES,
  PROJECTS,
  PROMPTS,
  THINGS,
  type ProjectVocab,
} from './vocab';

const MIN = 60_000;
const HOUR = 3_600_000;
const DAY = 86_400_000;
const KST_OFFSET = 9 * HOUR;

export interface BlueprintSegment {
  start: number;
  end: number;
}

export interface BlueprintInput {
  at: number;
  text: string;
  state: PreviewState;
  /** 작업 중에 들어온 짧은 입력(턴을 열지 않는다). */
  followUp: boolean;
}

export interface Blueprint {
  id: string;
  agent: 'claude' | 'codex' | 'gjc';
  model: string;
  profile: { name: string; loginEmail: string };
  project: AnalysisProject;
  branch: string;
  components: string[];
  screens: string[];
  kinds: string[];
  /** 완료 여부. 사실이 없는 세션은 null. */
  status: CalendarStatus | null;
  facts: AnalysisState;
  title: AnalysisTitle;
  segments: BlueprintSegment[];
  /** 사람 입력. 비어 있으면 요청이 없는 세션이다. */
  inputs: BlueprintInput[];
  requestless: boolean;
  planFile: boolean;
  planMode: boolean;
  costUsd: number | null;
  costSource: CostSource;
  tokens: { input: number; output: number; cache_read: number; cache_create: number };
  contextPercent: number | null;
  editedFiles: number;
  /** 이 세션을 만든 규칙의 이름(시험이 어떤 경우가 들어 있는지 확인한다). */
  scenario: string;
}

/** 서울 달력의 한 날 자정(절대 시각, ms). */
const kstMidnight = (year: number, month: number, day: number): number => Date.UTC(year, month - 1, day) - KST_OFFSET;
const kst = (year: number, month: number, day: number, hour: number, minute = 0): number =>
  kstMidnight(year, month, day) + hour * HOUR + minute * MIN;

/** 기준 시각이 든 서울 주의 월요일 자정(절대 시각). */
const mondayMidnightOf = (nowMs: number): number => {
  const local = new Date(nowMs + KST_OFFSET);
  const dow = local.getUTCDay();
  const back = (dow + 6) % 7;
  return Date.UTC(local.getUTCFullYear(), local.getUTCMonth(), local.getUTCDate() - back) - KST_OFFSET;
};

/** 주(몇 주 전)마다 요일별로 시작하는 세션 수. 월요일부터 일요일까지. */
const WEEK_PLAN: { weeksBack: number; counts: number[] }[] = [
  { weeksBack: 3, counts: [2, 3, 2, 2, 1, 0, 0] },
  { weeksBack: 2, counts: [3, 3, 3, 2, 2, 1, 0] },
  // 지난주: 레퍼런스의 한 주처럼 평일이 붐비고 토요일이 한산하며 일요일은 비어 있다.
  { weeksBack: 1, counts: [27, 24, 28, 25, 21, 7, 0] },
  // 이번 주: 기준 시각(토요일 오후)까지. 일요일은 아직 오지 않았다.
  { weeksBack: 0, counts: [20, 23, 22, 24, 19, 6, 0] },
];

const gauss = (rng: Rng): number => {
  const u = Math.max(1e-9, rng.next());
  const v = rng.next();
  return Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v);
};

/** 하루 중 시작 시각(분). 낮이 많고 밤에도 일부가 시작한다. */
const startMinute = (rng: Rng, saturday: boolean): number => {
  const bands = saturday
    ? [
        { value: [0, 300], weight: 10 },
        { value: [300, 600], weight: 6 },
        { value: [600, 840], weight: 24 },
        { value: [840, 1140], weight: 30 },
        { value: [1140, 1440], weight: 30 },
      ]
    : [
        { value: [0, 300], weight: 8 },
        { value: [300, 540], weight: 7 },
        { value: [540, 720], weight: 28 },
        { value: [720, 840], weight: 8 },
        { value: [840, 1140], weight: 32 },
        { value: [1140, 1440], weight: 17 },
      ];
  const [lo, hi] = rng.weighted(bands.map((b) => ({ value: b.value, weight: b.weight })));
  return rng.int(lo, hi - 1);
};

const pickProject = (rng: Rng): ProjectVocab => rng.weighted(PROJECTS.map((p) => ({ value: p, weight: p.weight })));

/** 앞의 값일수록 자주 나온다(1, 1/2, 1/3 ...). */
const pickZipf = <T>(rng: Rng, items: readonly T[]): T => rng.weighted(items.map((value, i) => ({ value, weight: 1 / (i + 1) })));

const promptOf = (rng: Rng, kind: string, thing: string): string => rng.pick(PROMPTS[kind] ?? PROMPTS['구현']).replace('{t}', thing);

const roundTo = (n: number, digits: number): number => {
  const k = 10 ** digits;
  return Math.round(n * k) / k;
};

interface IdPool {
  used: Set<string>;
}

/** 앞 8자가 겹치지 않는 UUID 꼴 id. 목록과 뷰어가 짧은 ID로 세션을 알아본다. */
const newId = (rng: Rng, pool: IdPool): string => {
  for (;;) {
    const head = rng.hex(8);
    if (pool.used.has(head)) continue;
    pool.used.add(head);
    return `${head}-${rng.hex(4)}-4${rng.hex(3)}-${rng.pick(['8', '9', 'a', 'b'])}${rng.hex(3)}-${rng.hex(12)}`;
  }
};

interface Spec {
  /** 시작(절대 시각). */
  start: number;
  /** 기준 시각. 세션은 이 시각을 넘어서 활동하지 않는다. */
  now: number;
  marathon?: boolean;
  requestless?: boolean;
  /** 자정을 넘기도록 길게. */
  scenario: string;
}

/** 조각 시각을 짠다: 활동 총 길이를 k조각으로 나누고 사이에 15분 이상의 공백을 둔다. */
const planSegments = (rng: Rng, spec: Spec, activeMin: number): BlueprintSegment[] => {
  const k = spec.marathon ? 3 : rng.weighted([
    { value: 1, weight: 60 },
    { value: 2, weight: 22 },
    { value: 3, weight: 10 },
    { value: 4, weight: 5 },
    { value: 5, weight: 3 },
  ]);
  const weights = Array.from({ length: k }, () => rng.between(0.4, 1.6));
  const total = weights.reduce((s, w) => s + w, 0);
  const segments: BlueprintSegment[] = [];
  let cursor = spec.start;
  for (let i = 0; i < k; i++) {
    const length = Math.max(2, Math.round((activeMin * weights[i]) / total)) * MIN;
    segments.push({ start: cursor, end: cursor + length });
    // 공백: 대개 16~70분, 가끔 2~6시간(자리를 오래 비웠다 돌아온 경우).
    const gap = rng.chance(0.2) ? rng.between(120, 360) : rng.between(16, 70);
    cursor += length + Math.round(gap) * MIN;
  }
  return segments;
};

/** 며칠 뒤에 다시 이어 한 조각들(resume). */
const resumeSegments = (rng: Rng, last: BlueprintSegment, now: number): BlueprintSegment[] => {
  const extra: BlueprintSegment[] = [];
  const days = rng.int(1, 3);
  const dayStart = Math.floor((last.end + KST_OFFSET) / DAY) * DAY - KST_OFFSET + days * DAY;
  const start = dayStart + rng.int(9, 19) * HOUR + rng.int(0, 59) * MIN;
  const end = start + rng.int(6, 45) * MIN;
  if (end < now - 10 * MIN) extra.push({ start, end });
  return extra;
};

/** 조각을 기준 시각 안으로 접는다. 시작이 기준 시각 이후인 조각은 버린다. */
const clampToNow = (segments: BlueprintSegment[], now: number): BlueprintSegment[] =>
  segments
    .filter((s) => s.start < now - 8 * MIN)
    .map((s) => ({ start: s.start, end: Math.min(s.end, now - 2 * MIN) }))
    .filter((s) => s.end >= s.start);

const planInputs = (rng: Rng, segments: BlueprintSegment[], kind: string, thing: string, rate: [number, number], previewState: PreviewState): BlueprintInput[] => {
  const inputs: BlueprintInput[] = [];
  let first = true;
  for (const seg of segments) {
    const lengthMin = (seg.end - seg.start) / MIN;
    const count = Math.max(1, Math.round(lengthMin / rng.between(rate[0], rate[1])));
    const offsets = new Set<number>([Math.round(rng.between(0, Math.min(1.5, lengthMin)) * 60)]);
    for (let i = 1; i < count; i++) offsets.add(Math.round(rng.between(0, lengthMin) * 60));
    for (const sec of [...offsets].sort((a, b) => a - b)) {
      const at = Math.min(seg.end, seg.start + sec * 1000);
      const newPrompt = first || rng.chance(0.38);
      inputs.push({
        at,
        text: newPrompt ? promptOf(rng, kind, first ? thing : rng.pick(THINGS)) : rng.pick(FOLLOW_UPS),
        state: previewState,
        followUp: !newPrompt,
      });
      first = false;
    }
  }
  return inputs;
};

const titleOf = (rng: Rng, kind: string, thing: string, firstPrompt: string | null): AnalysisTitle => {
  const suffix = KINDS.find((k) => k.value === kind)?.suffix ?? '정리';
  const roll = rng.next();
  if (roll < 0.04) return { text: null, source: 'none', state: 'none' };
  if (roll < 0.06) return { text: null, source: 'none', state: 'redacted' };
  if (roll < 0.1 && firstPrompt) return { text: firstPrompt, source: 'first_request', state: 'withheld' };
  return { text: `${thing} ${suffix}`, source: rng.chance(0.7) ? 'ai' : 'custom', state: 'ok' };
};

const buildBlueprint = (rng: Rng, pool: IdPool, spec: Spec): Blueprint | null => {
  const vocab = pickProject(rng);
  const kind = rng.weighted(KINDS.map((k) => ({ value: k.value, weight: k.weight })));
  const secondKind = rng.chance(0.3) ? rng.weighted(KINDS.filter((k) => k.value !== kind).map((k) => ({ value: k.value, weight: k.weight }))) : null;
  const thing = rng.pick(THINGS);
  const screen = rng.chance(0.08) ? null : pickZipf(rng, vocab.screens);
  const component = rng.chance(0.1) ? null : pickZipf(rng, vocab.components);
  const isCodex = !spec.requestless && rng.chance(0.2);
  const agent: Blueprint['agent'] = isCodex ? 'codex' : 'claude';
  const model = isCodex ? rng.weighted(CODEX_MODELS) : rng.weighted(CLAUDE_MODELS);
  const profile = isCodex ? PROFILES[2] : rng.chance(0.62) ? PROFILES[0] : PROFILES[1];

  const activeMin = spec.marathon
    ? rng.int(250, 330)
    : Math.round(Math.min(240, Math.max(3, Math.exp(Math.log(18) + 0.85 * gauss(rng)))));
  let segments = planSegments(rng, spec, activeMin);
  if (!spec.marathon && rng.chance(0.1)) segments = [...segments, ...resumeSegments(rng, segments[segments.length - 1], spec.now)];
  segments = clampToNow(segments, spec.now);
  if (segments.length === 0) return null;

  // 분석 사실 상태: 대부분 ready, 일부는 사실이 없다(범위만 흐리게 그린다).
  const facts: AnalysisState = spec.requestless
    ? 'ready'
    : rng.weighted<AnalysisState>([
        { value: 'ready', weight: 93 },
        { value: 'stale', weight: 2 },
        { value: 'truncated', weight: 2 },
        { value: 'building', weight: 1 },
        { value: 'none', weight: 2 },
      ]);
  const hasFacts = facts === 'ready' || facts === 'stale';

  const previewState: PreviewState = rng.weighted<PreviewState>([
    { value: 'ok', weight: 92 },
    { value: 'redacted', weight: 4 },
    { value: 'withheld', weight: 4 },
  ]);
  const rate: [number, number] = spec.marathon ? [1.1, 1.7] : [3, 9];
  const inputs = spec.requestless || !hasFacts ? [] : planInputs(rng, segments, kind, thing, rate, previewState);
  const requestless = spec.requestless === true;
  const requests = inputs.filter((i) => !i.followUp).length;

  const statusRoll = rng.weighted<CalendarStatus>([
    { value: 'done', weight: 40 },
    { value: 'leftover', weight: 54 },
    { value: 'midway', weight: 6 },
  ]);
  const status: CalendarStatus | null = hasFacts ? statusRoll : null;

  const planFile = !requestless && rng.chance(0.08);
  const costBase = requestless ? rng.between(0.02, 0.3) : requests * rng.between(0.12, 0.55);
  const input = Math.round(Math.max(1, requests) * rng.between(8_000, 30_000));
  const firstPrompt = inputs.find((i) => !i.followUp)?.text ?? null;

  return {
    id: newId(rng, pool),
    agent,
    model,
    profile,
    project: vocab.project,
    branch: rng.pick(BRANCHES),
    components: component ? [component] : [],
    screens: screen ? [screen] : [],
    kinds: secondKind ? [kind, secondKind] : [kind],
    status,
    facts,
    title: titleOf(rng, kind, thing, previewState === 'ok' ? firstPrompt : null),
    segments,
    inputs,
    requestless,
    planFile,
    planMode: planFile || rng.chance(0.12),
    costUsd: facts === 'building' ? null : roundTo(costBase, 4),
    costSource: isCodex ? 'estimate' : 'otel',
    tokens: {
      input,
      output: Math.round(input * rng.between(0.12, 0.3)),
      cache_read: Math.round(input * rng.between(15, 40)),
      cache_create: Math.round(input * rng.between(0.5, 2)),
    },
    contextPercent: hasFacts ? Math.round(rng.between(8, 72)) : null,
    editedFiles: requestless ? 0 : rng.int(0, kind === '구현' ? 12 : 5),
    scenario: spec.scenario,
  };
};

/** 눈으로 확인해야 할 경우를 빠뜨리지 않도록 직접 짠 세션들. */
const handmade = (rng: Rng, pool: IdPool, mondayMs: number, nowMs: number): Blueprint[] => {
  const day = (weeksBack: number, dow: number): number => mondayMs - weeksBack * 7 * DAY + dow * DAY;
  const at = (weeksBack: number, dow: number, hour: number, minute = 0): number => day(weeksBack, dow) + hour * HOUR + minute * MIN;
  const web = PROJECTS[0];
  const billing = PROJECTS[1];
  const make = (
    scenario: string,
    over: Partial<Blueprint> & { segments: BlueprintSegment[]; inputs?: BlueprintInput[] },
    kind = '구현',
  ): Blueprint => {
    const thing = rng.pick(THINGS);
    const inputs =
      over.inputs ??
      planInputs(rng, over.segments, kind, thing, [4, 8], 'ok');
    return {
      id: newId(rng, pool),
      agent: 'claude',
      model: 'claude-opus-5',
      profile: PROFILES[0],
      project: web.project,
      branch: 'main',
      components: ['웹'],
      screens: ['대시보드'],
      kinds: [kind],
      status: 'leftover',
      facts: 'ready',
      title: { text: `${thing} ${KINDS.find((k) => k.value === kind)?.suffix ?? '정리'}`, source: 'ai', state: 'ok' },
      requestless: false,
      planFile: false,
      planMode: false,
      costUsd: roundTo(inputs.length * 0.31, 4),
      costSource: 'otel',
      tokens: { input: inputs.length * 14_000, output: inputs.length * 2_800, cache_read: inputs.length * 330_000, cache_create: inputs.length * 12_000 },
      contextPercent: 34,
      editedFiles: 4,
      scenario,
      ...over,
      inputs,
    };
  };

  const list: Blueprint[] = [];

  // 자정을 넘는 세션: 이번 주 화요일 22:20 ~ 수요일 01:40.
  list.push(
    make('midnight', {
      segments: [
        { start: at(0, 1, 22, 20), end: at(0, 1, 23, 40) },
        { start: at(0, 2, 0, 5), end: at(0, 2, 1, 40) },
      ],
      title: { text: '야간 배치 시간대 점검', source: 'ai', state: 'ok' },
      status: 'done',
      project: billing.project,
      components: ['배치'],
      screens: ['정산'],
      kinds: ['조사'],
    }),
  );

  // 며칠에 걸쳐 긴 공백으로 이어 한 세션: 지난주 수·금, 이번 주 월·화.
  list.push(
    make('resumed', {
      segments: [
        { start: at(1, 2, 11, 0), end: at(1, 2, 11, 40) },
        { start: at(1, 4, 15, 0), end: at(1, 4, 15, 30) },
        { start: at(0, 0, 9, 30), end: at(0, 0, 10, 0) },
        { start: at(0, 1, 20, 0), end: at(0, 1, 20, 50) },
      ],
      title: { text: '쿠폰 적용 순서 이어서 정리', source: 'custom', state: 'ok' },
      kinds: ['구현', '상담'],
    }),
  );

  // 입력이 수백 개인 세션: 이번 주 목요일 오전 6시간.
  const marathonSegments = [{ start: at(0, 3, 9, 10), end: at(0, 3, 15, 25) }];
  const marathonInputs: BlueprintInput[] = [];
  for (let t = marathonSegments[0].start + 20_000, i = 0; t < marathonSegments[0].end; t += Math.round(rng.between(70, 120) * 1000), i++) {
    marathonInputs.push({ at: t, text: i % 5 === 0 ? `로그 묶음 ${i + 1}번 정리하고 요약 갱신해줘` : rng.pick(FOLLOW_UPS), state: 'ok', followUp: i % 5 !== 0 });
  }
  list.push(
    make('marathon', {
      segments: marathonSegments,
      inputs: marathonInputs,
      title: { text: '로그 묶음 정리와 요약 표 갱신', source: 'ai', state: 'ok' },
      project: PROJECTS[2].project,
      components: ['배치'],
      screens: ['리포트'],
      kinds: ['정비'],
      model: 'claude-sonnet-5',
    }),
  );

  // 다섯 세션이 한꺼번에 겹치는 시간: 이번 주 월요일 10:30~11:05.
  const clusterProjects = [PROJECTS[0], PROJECTS[1], PROJECTS[3], PROJECTS[4], PROJECTS[5]];
  clusterProjects.forEach((p, i) => {
    list.push(
      make(
        'cluster',
        {
          segments: [{ start: at(0, 0, 10, 28 + i * 2), end: at(0, 0, 11, 2 + i * 3) }],
          project: p.project,
          components: [p.components[0]],
          screens: [p.screens[0]],
          profile: i % 2 === 0 ? PROFILES[0] : PROFILES[1],
          status: i === 0 ? 'done' : 'leftover',
        },
        i % 2 === 0 ? '구현' : '버그 수정',
      ),
    );
  });

  // 짧은 세션(20분 규칙): 월요일 10:31에 4분.
  list.push(make('short', { segments: [{ start: at(0, 0, 10, 31), end: at(0, 0, 10, 35) }], status: 'done' }, '조사'));

  // 입력 첫 줄이 가려진 세션(비식별화)과 서버에 올리지 않은 세션(보류).
  list.push(
    make('redacted-previews', {
      segments: [{ start: at(0, 1, 13, 0), end: at(0, 1, 14, 20) }],
      title: { text: null, source: 'none', state: 'redacted' },
      inputs: planInputs(rng, [{ start: at(0, 1, 13, 0), end: at(0, 1, 14, 20) }], '구현', '주문 목록 필터', [5, 9], 'redacted'),
      status: 'leftover',
    }),
  );
  list.push(
    make('withheld-previews', {
      segments: [{ start: at(0, 2, 16, 0), end: at(0, 2, 17, 10) }],
      title: { text: null, source: 'none', state: 'withheld' },
      inputs: planInputs(rng, [{ start: at(0, 2, 16, 0), end: at(0, 2, 17, 10) }], '조사', '재고 동기화', [5, 9], 'withheld'),
      status: 'done',
    }),
  );

  // 사실이 잘려 범위만 있는 세션(truncated).
  list.push({
    ...make('truncated', { segments: [{ start: at(0, 2, 8, 30), end: at(0, 3, 4, 10) }] }),
    facts: 'truncated',
    status: null,
    inputs: [],
    segments: [{ start: at(0, 2, 8, 30), end: at(0, 3, 4, 10) }],
    contextPercent: null,
    title: { text: null, source: 'none', state: 'none' },
    components: [],
    screens: [],
    kinds: [],
  });

  // 지금 일하고 있는 세션 둘(기준 시각 직전까지).
  for (let i = 0; i < 2; i++) {
    list.push(
      make(
        'running',
        {
          segments: [{ start: nowMs - (62 + i * 25) * MIN, end: nowMs - 2 * MIN }],
          status: 'running',
          profile: i === 0 ? PROFILES[0] : PROFILES[1],
          project: i === 0 ? PROJECTS[1].project : PROJECTS[0].project,
          components: [i === 0 ? '결제 모듈' : '웹'],
          screens: [i === 0 ? '결제' : '주문 관리'],
        },
        '구현',
      ),
    );
  }
  return list;
};

/** 합성 주 전체. 서로 다른 세션 약 330건이고 이번 주와 지난주가 각각 150건 안팎이다. */
const generateBlueprints = (nowMs: number, seed = 20261010): Blueprint[] => {
  const rng = createRng(seed);
  const pool: IdPool = { used: new Set<string>() };
  const monday = mondayMidnightOf(nowMs);
  const blueprints: Blueprint[] = [];

  for (const week of WEEK_PLAN) {
    week.counts.forEach((count, dow) => {
      const dayStart = monday - week.weeksBack * 7 * DAY + dow * DAY;
      for (let i = 0; i < count; i++) {
        // 오늘의 세션은 기준 시각보다 앞에서 시작해야 한다.
        const horizon = Math.min(DAY, nowMs - dayStart - 12 * MIN);
        if (horizon <= 0) continue;
        const requestless = rng.chance(0.1);
        let minute = startMinute(rng, dow === 5);
        if (minute * MIN >= horizon) minute = Math.floor(rng.between(0, horizon / MIN));
        const spec: Spec = { start: dayStart + minute * MIN, now: nowMs, requestless, scenario: requestless ? 'requestless' : 'ordinary' };
        const built = buildBlueprint(rng, pool, spec);
        if (built) blueprints.push(built);
      }
    });
  }
  blueprints.push(...handmade(rng, pool, monday, nowMs));
  // 지난주 일요일은 비어 있어야 한다("활동이 없는 날"). 그 날에 걸친 조각과 입력을 뺀다.
  const emptyFrom = monday - DAY;
  const emptyTo = monday;
  const kept = blueprints.flatMap((bp) => {
    const segments = bp.segments.filter((s) => !(s.start < emptyTo && s.end > emptyFrom));
    if (segments.length === 0) return [];
    const inputs = bp.inputs.filter((i) => !(i.at >= emptyFrom && i.at < emptyTo));
    return [{ ...bp, segments, inputs }];
  });
  return kept.sort((a, b) => a.segments[0].start - b.segments[0].start || (a.id < b.id ? -1 : 1));
};

export { MIN, HOUR, DAY, KST_OFFSET, kst, kstMidnight, mondayMidnightOf, generateBlueprints };
