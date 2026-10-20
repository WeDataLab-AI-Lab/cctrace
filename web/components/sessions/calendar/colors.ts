/**
 * 색 기준과 범례(C4, C5).
 *
 * 색 기준은 레퍼런스의 다섯 — 완료 여부, 컴포넌트, 화면, 종별, 프로필 — 이 앞에 오고 프로젝트, 에이전트, 모델이 뒤따른다.
 * 고른 기준으로 **그 주 세션**을 값별로 센다. 범례는 모든 값을 세션 수가 많은 순(같으면 이름 순)으로 보이고, 값이 없는 세션은
 * "(없음)" 칸으로 센다 — 이 칸도 개수 순서에 들어간다. 범례의 합은 그 주의 세션 수와 같다(세션마다 칸이 정확히 하나).
 *
 * 컴포넌트·화면·종별은 세션이 값을 여러 개 가질 수 있다. 색과 범례는 **첫 값**만 쓴다(합이 세션 수와 같아야 하므로).
 * 나머지 값은 호버에 보인다.
 *
 * 색: 완료 여부는 값마다 고정된 색이다. 나머지는 개수 순위대로 범주 색(`--cat-1`~`--cat-8`)을 준다. 값이 8개를 넘으면
 * 같은 색을 어둡게(9~16위), 다시 밝게(17~24위) 바꿔 쓴다 — 레퍼런스도 값이 많은 주에는 색이 겹친다. 범례에 올리면 같은 값의
 * 띠가 강조되므로(호버) 겹친 색도 가를 수 있다. "(없음)"은 중립 회색이다.
 */

import type { CalendarColorKey, CalendarSession, CalendarStatus } from '@/lib/session-calendar-types';
import { STATUS_META } from './status';

/** "색 기준" 선택지의 순서와 이름 (계획 §3.1 C4). */
const COLOR_KEYS: readonly { key: CalendarColorKey; label: string; hint: string }[] = [
  { key: 'status', label: '완료 여부', hint: '완료, 하다 만 것, 도중 종료, 실행 중. 세션 주인만 볼 수 있습니다.' },
  { key: 'component', label: '코드 영역', hint: '세션 기록의 코드 영역 태그로 구분합니다. 여러 태그가 있으면 첫 번째를 사용합니다.' },
  { key: 'screen', label: '대상 화면', hint: '세션 기록의 작업 대상 화면 태그로 구분합니다. 여러 태그가 있으면 첫 번째를 사용합니다.' },
  { key: 'kind', label: '작업 유형', hint: '구현·조사·정비·상담·버그 수정·문서 같은 작업 유형으로 구분합니다. 여러 태그가 있으면 첫 번째를 사용합니다.' },
  { key: 'profile', label: '프로필', hint: '세션이 쓴 계정 프로필' },
  { key: 'project', label: '프로젝트', hint: '세션이 일한 프로젝트' },
  { key: 'agent', label: '에이전트', hint: 'Claude Code, Codex 등' },
  { key: 'model', label: '모델', hint: '세션의 모델' },
];

/** 값이 없는 세션을 세는 칸의 이름과 키. */
const NONE_LABEL = '(없음)';
const NONE_KEY = '\u0000none';
const NONE_TOKEN = 'var(--ink-4)';

/** 범주 색 토큰의 수. 디자인 시스템의 `--cat-1`~`--cat-8`이다. */
const BASE_COLORS = 8;

/**
 * 순위(1부터) → 색. 1~8위는 범주 색 그대로, 9~16위는 어둡게, 17~24위는 밝게, 그 뒤는 처음부터 돈다.
 * 변형은 토큰을 섞어 만든다(새 색상 값을 쓰지 않는다).
 */
const paletteToken = (rank: number): string => {
  const index = (rank - 1) % (BASE_COLORS * 3);
  const base = `var(--cat-${(index % BASE_COLORS) + 1})`;
  if (index < BASE_COLORS) return base;
  if (index < BASE_COLORS * 2) return `color-mix(in oklch, ${base} 68%, black)`;
  return `color-mix(in oklch, ${base} 52%, white)`;
};

export interface ColorValueRef {
  key: string;
  label: string;
}

/** 세션의 색 기준 값. 값이 없으면 null(범례의 "(없음)"). */
const colorValueOf = (session: CalendarSession, colorKey: CalendarColorKey): ColorValueRef | null => {
  const colors = session.colors;
  if (!colors) return null;
  const text = (value: string | null | undefined): ColorValueRef | null => (typeof value === 'string' && value ? { key: value, label: value } : null);
  switch (colorKey) {
    case 'status': {
      const status = colors.status;
      return status ? { key: STATUS_META[status] ? status : 'unknown', label: STATUS_META[status]?.label ?? STATUS_META.unknown.label } : null;
    }
    case 'component':
      return text(Array.isArray(colors.components) ? colors.components[0] : null);
    case 'screen':
      return text(Array.isArray(colors.screens) ? colors.screens[0] : null);
    case 'kind':
      return text(Array.isArray(colors.kinds) ? colors.kinds[0] : null);
    case 'profile':
      return text(colors.profile);
    case 'project':
      return colors.project?.hash ? { key: colors.project.hash, label: colors.project.name || colors.project.hash } : null;
    case 'agent':
      return text(colors.agent);
    case 'model':
      return text(colors.model);
  }
};

export interface LegendEntry {
  /** 값의 키. "(없음)" 칸은 `NONE_KEY`다. */
  key: string;
  label: string;
  /** 그 주에 이 값을 가진 세션 수. */
  sessions: number;
  /** 색(CSS 값). 띠와 범례 점이 같은 값을 쓴다. */
  token: string;
  /** 값이 없는 세션을 세는 칸인가. */
  none: boolean;
}

export interface SessionColor {
  /** 이 세션이 속한 범례 칸의 키. */
  entryKey: string;
  token: string;
  none: boolean;
}

export interface ColorAssignment {
  colorKey: CalendarColorKey;
  /** 모든 칸. 세션 수 많은 순, 같으면 이름 순이다. "(없음)"도 이 순서에 든다. */
  legend: LegendEntry[];
  /** 그 주의 세션 수. 범례의 합과 같다. */
  total: number;
  /** 세션 id → 색. */
  bySession: Map<string, SessionColor>;
}

const compareEntries = (a: LegendEntry, b: LegendEntry): number => {
  if (b.sessions !== a.sessions) return b.sessions - a.sessions;
  // 개수가 같으면 값이 있는 칸이 먼저, 그다음 이름 순이다.
  if (a.none !== b.none) return a.none ? 1 : -1;
  return a.label < b.label ? -1 : a.label > b.label ? 1 : a.key < b.key ? -1 : a.key > b.key ? 1 : 0;
};

const assignColors = (sessions: readonly CalendarSession[], colorKey: CalendarColorKey): ColorAssignment => {
  const tally = new Map<string, { key: string; label: string; sessions: number }>();
  const valueOfSession = new Map<string, string>();
  for (const session of sessions) {
    const value = colorValueOf(session, colorKey);
    const key = value ? value.key : NONE_KEY;
    valueOfSession.set(session.session_id, key);
    const entry = tally.get(key);
    if (entry) entry.sessions += 1;
    else tally.set(key, { key, label: value ? value.label : NONE_LABEL, sessions: 1 });
  }

  // 색은 값이 있는 칸의 순위로 정한다. 완료 여부는 값마다 고정이다.
  const real = [...tally.values()]
    .filter((e) => e.key !== NONE_KEY)
    .map<LegendEntry>((e) => ({ ...e, token: '', none: false }))
    .sort(compareEntries);
  real.forEach((entry, i) => {
    entry.token = colorKey === 'status' ? STATUS_META[entry.key as CalendarStatus].token : paletteToken(i + 1);
  });
  const none = tally.get(NONE_KEY);
  const legend = [...real, ...(none ? [{ ...none, token: NONE_TOKEN, none: true }] : [])].sort(compareEntries);

  const byKey = new Map(legend.map((e) => [e.key, e]));
  const bySession = new Map<string, SessionColor>();
  for (const session of sessions) {
    const entry = byKey.get(valueOfSession.get(session.session_id) ?? NONE_KEY);
    if (entry) bySession.set(session.session_id, { entryKey: entry.key, token: entry.token, none: entry.none });
  }
  return { colorKey, legend, total: sessions.length, bySession };
};

/** 색 기준을 쓸 수 있는가. 완료 여부는 세션 주인 범위에서만 온다. */
const colorKeyAvailable = (colorKey: CalendarColorKey, ownerView: boolean): boolean => colorKey !== 'status' || ownerView;

export { COLOR_KEYS, NONE_LABEL, NONE_KEY, NONE_TOKEN, BASE_COLORS, paletteToken, colorValueOf, assignColors, colorKeyAvailable };
