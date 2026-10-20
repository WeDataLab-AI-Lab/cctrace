import { describe, expect, it } from 'vitest';
import type {
  AnalysisTurn,
  JudgementCheckKey,
  RecordState,
  RecordWithContent,
  SessionAnalysisFull,
  SessionRecord,
  TimelineMessage,
} from '@/lib/session-analysis-types';
import { isFullAnalysis } from '@/components/sessions/analysis/guards';
import { PREVIEW_NOW, totalOf } from '../builders';
import { PREVIEW_SESSIONS } from '../fixtures';
import { TIMELINE_WINDOW, isAliveCategory, isInterruptedSignal } from './adapt';
import {
  DETAIL_NOW,
  DETAIL_SESSIONS,
  FLAGSHIP_ID,
  FRAME_ACCOUNT_LABELS,
  SEED_GLOBAL_NOTE,
  SEED_NOTES,
  analysisFor,
  frameFor,
  rowFor,
  timelineFor,
  type DetailFixture,
} from './fixtures';
import { deriveJudgement } from './judgement-derive';
import {
  ALL_PASS_ID,
  CODEX_ID,
  ENDED_MIDWAY_ID,
  INTERRUPTED_ID,
  LEFTOVER_ID,
  NO_RECORD_ID,
  PROCESSING_ID,
  RECORD_FAILED_ID,
  RECORD_GENERATING_ID,
  RECORD_REDACTED_ID,
  RECORD_STALE_ID,
  RECORD_WITHHELD_ID,
} from './variants';

const ORDER: readonly JudgementCheckKey[] = ['turn_ended', 'no_pending_reply', 'no_background', 'committed', 'work_done'];

interface Full {
  f: DetailFixture;
  a: SessionAnalysisFull;
}

const fulls: Full[] = DETAIL_SESSIONS.flatMap((f) => (f.analysis !== null && isFullAnalysis(f.analysis) ? [{ f, a: f.analysis }] : []));

const hasContent = (r: SessionRecord | undefined): r is RecordWithContent => r !== undefined && (r.state === 'ready' || r.state === 'stale');

const byId = (id: string): Full => {
  const found = fulls.find((x) => x.f.id === id);
  if (!found) throw new Error(`fixture ${id} is not a full analysis`);
  return found;
};

const allTurns = (a: SessionAnalysisFull): AnalysisTurn[] => [...a.turns, ...a.abandoned];

/** 값을 깊이 따라가며 방문한다. 순환은 없는 JSON 모양이다. */
const walk = (value: unknown, path: string, visit: (v: unknown, path: string) => void): void => {
  visit(value, path);
  if (Array.isArray(value)) {
    value.forEach((v, i) => walk(v, `${path}[${i}]`, visit));
  } else if (typeof value === 'object' && value !== null) {
    const entries: [string, unknown][] = Object.entries(value);
    for (const [key, child] of entries) walk(child, `${path}.${key}`, visit);
  }
};

const keyOfCommit = (short: string | null, subject: string): string => `${short ?? ''}|${subject}`;

describe('세션 목록의 구성', () => {
  it('DETAIL_NOW는 패스 A·캘린더와 같은 기준 시각이다', () => {
    expect(DETAIL_NOW).toBe(PREVIEW_NOW);
  });

  it('대표 하나, 변형 열셋, 패스 A 전부', () => {
    expect(DETAIL_SESSIONS).toHaveLength(1 + 13 + PREVIEW_SESSIONS.length);
    expect(DETAIL_SESSIONS[0].id).toBe(FLAGSHIP_ID);
    const ids = new Set(DETAIL_SESSIONS.map((f) => f.id));
    for (const p of PREVIEW_SESSIONS) expect(ids.has(p.id), p.id).toBe(true);
    for (const id of [ALL_PASS_ID, LEFTOVER_ID, PROCESSING_ID, INTERRUPTED_ID, NO_RECORD_ID, RECORD_STALE_ID, RECORD_GENERATING_ID, RECORD_WITHHELD_ID, RECORD_REDACTED_ID, RECORD_FAILED_ID, ENDED_MIDWAY_ID, CODEX_ID]) {
      expect(ids.has(id), id).toBe(true);
    }
  });

  it('세션 ID는 모두 다르고 앞 8자도 겹치지 않는다(목록과 짧은 ID)', () => {
    const ids = DETAIL_SESSIONS.map((f) => f.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(new Set(ids.map((id) => id.slice(0, 8))).size).toBe(ids.length);
  });

  it('행과 응답의 ID가 맞고, 모든 세션에 라벨과 덮는 것이 적혀 있다', () => {
    for (const f of DETAIL_SESSIONS) {
      expect(f.row.session_id, f.id).toBe(f.id);
      if (f.analysis) expect(f.analysis.session_id, f.id).toBe(f.id);
      expect(f.label.length, f.id).toBeGreaterThan(10);
      expect(f.covers.length, f.id).toBeGreaterThan(0);
    }
  });
});

describe('판정', () => {
  it('판정 다섯은 정해진 순서로 정확히 다섯 개다(주인 응답)', () => {
    expect(fulls.filter(({ a }) => a.judgement !== undefined).length).toBeGreaterThan(20);
    for (const { f, a } of fulls) {
      if (!a.judgement) continue;
      expect(a.judgement.checks.map((c) => c.key), f.id).toEqual(ORDER);
    }
  });

  it('목록 행의 판정은 상태와 다섯 판정의 상태이고 응답과 같다', () => {
    for (const { f, a } of fulls) {
      const row = f.row.analysis?.judgement;
      if (!a.judgement) {
        expect(row, f.id).toBeUndefined();
        continue;
      }
      expect(row?.checks, f.id).toHaveLength(5);
      expect(row?.checks, f.id).toEqual(a.judgement.checks.map((c) => c.state));
      expect(row?.status, f.id).toBe(a.judgement.status);
      expect(row?.bucket, f.id).toBe(a.judgement.bucket);
    }
  });

  it('모든 응답이 패스 C 필드를 가진다(주인 범위)', () => {
    for (const { f, a } of fulls) {
      expect(a.judgement, f.id).toBeDefined();
      expect(a.record, f.id).toBeDefined();
      expect(a.commits, f.id).toBeDefined();
      expect(a.tasks, f.id).toBeDefined();
      expect(a.classification, f.id).toBeDefined();
      expect(a.note === null || typeof a.note?.text === 'string', f.id).toBe(true);
    }
  });

  it('판정은 그 응답의 신호·기록·관측 범주로 다시 계산한 값과 같다', () => {
    for (const { f, a } of fulls) {
      const alive = isAliveCategory(a.header.category?.key);
      const again = deriveJudgement({ alive, interrupted: alive && isInterruptedSignal(a.signals), signals: a.signals, record: a.record });
      expect(a.judgement, f.id).toEqual(again);
    }
  });

  it('실패 판정은 건수를, 판단 불가 판정은 이유를 가진다. 통과는 둘 다 없다', () => {
    for (const { f, a } of fulls) {
      for (const c of a.judgement?.checks ?? []) {
        if (c.state === 'pass') {
          expect(c.count, `${f.id}:${c.key}`).toBeUndefined();
          expect(c.reason, `${f.id}:${c.key}`).toBeUndefined();
        }
        if (c.state === 'undeterminable') expect(c.reason, `${f.id}:${c.key}`).toBeDefined();
        if (c.state === 'fail' && c.key !== 'turn_ended') expect(c.count, `${f.id}:${c.key}`).toBeGreaterThan(0);
        if (c.state !== 'fail') expect(c.count, `${f.id}:${c.key}`).toBeUndefined();
      }
    }
  });

  it('세션 기록에 본문이 없으면 작업 내용은 no_record, 있으면 최상위 항목으로 정해진다', () => {
    for (const { f, a } of fulls) {
      const work = a.judgement?.checks[4];
      if (!hasContent(a.record)) {
        expect(work, f.id).toMatchObject({ key: 'work_done', state: 'undeterminable', reason: 'no_record' });
      } else {
        const open = a.record.work.filter((w) => w.done === false).length;
        if (open > 0) expect(work, f.id).toMatchObject({ state: 'fail', count: open });
        else if (a.record.work.length === 0 || a.record.work.some((w) => w.done === null)) expect(work, f.id).toMatchObject({ state: 'undeterminable', reason: 'no_list' });
        else expect(work, f.id).toMatchObject({ state: 'pass' });
      }
    }
  });

  it('행의 headline은 기록의 굵은 한 줄이고, 본문이 없으면 null이다', () => {
    for (const { f, a } of fulls) {
      expect(f.row.analysis?.headline, f.id).toBe(hasContent(a.record) ? a.record.headline : null);
    }
  });

  it('행의 tokens_total은 총 토큰이고, commits는 관측한 수 또는 null이다', () => {
    for (const { f, a } of fulls) {
      expect(f.row.analysis?.tokens_total, f.id).toBe(totalOf(a.header.tokens));
      const expected = a.commits?.state === 'observed' ? (a.commits.total ?? a.commits.items.length) : null;
      expect(f.row.analysis?.commits, f.id).toBe(expected);
    }
  });
});

describe('커밋, task, 작업 항목은 서로를 가리킨다', () => {
  const shortsOf = (a: SessionAnalysisFull): string[] => (a.commits?.items ?? []).flatMap((c) => (c.short ? [c.short] : []));
  const taskIdsOf = (a: SessionAnalysisFull): string[] => (a.tasks?.items ?? []).map((t) => t.id);

  it('작업 항목의 커밋은 모두 세션의 커밋 목록에 있다', () => {
    for (const { f, a } of fulls) {
      if (!hasContent(a.record)) continue;
      const shorts = shortsOf(a);
      for (const w of a.record.work) for (const s of w.commits) expect(shorts, `${f.id}:${w.id}:${s}`).toContain(s);
      for (const b of a.record.bullets) for (const s of b.commits) expect(shorts, `${f.id}:bullet:${s}`).toContain(s);
    }
  });

  it('작업 항목과 커밋의 task는 task 목록에 있고, 작업 항목의 턴은 응답 안에 있다', () => {
    for (const { f, a } of fulls) {
      const ids = taskIdsOf(a);
      const keys = allTurns(a).map((t) => t.key);
      for (const c of a.commits?.items ?? []) for (const t of c.tasks) expect(ids, `${f.id}:commit:${t}`).toContain(t);
      if (!hasContent(a.record)) continue;
      for (const w of a.record.work) {
        for (const t of w.tasks) expect(ids, `${f.id}:${w.id}:${t}`).toContain(t);
        if (w.turn_key !== undefined) expect(keys, `${f.id}:${w.id}`).toContain(w.turn_key);
      }
    }
  });

  it('해시를 모르는 커밋은 어떤 작업 항목이나 글머리표에도 참조되지 않는다', () => {
    for (const { f, a } of fulls) {
      const unknown = (a.commits?.items ?? []).filter((c) => c.short === null);
      for (const c of unknown) expect(c.hash, f.id).toBeNull();
      if (unknown.length > 0 && hasContent(a.record)) {
        for (const w of a.record.work) expect(w.commits.every((s) => s.length > 0), f.id).toBe(true);
      }
    }
  });

  it('턴의 커밋은 세션 커밋 중 턴이 있는 것의 분할이다(각 커밋은 자기 턴에 한 번)', () => {
    for (const { f, a } of fulls) {
      const items = a.commits?.items ?? [];
      const attributed = items.filter((c) => c.turn_key !== undefined);
      const listed: string[] = [];
      for (const t of allTurns(a)) {
        const mine = attributed.filter((c) => c.turn_key === t.key).map((c) => keyOfCommit(c.short, c.subject));
        const shown = (t.commits ?? []).map((c) => keyOfCommit(c.short, c.subject));
        expect([...shown].sort(), `${f.id}:${t.key}`).toEqual([...mine].sort());
        listed.push(...shown);
      }
      expect([...listed].sort(), f.id).toEqual(attributed.map((c) => keyOfCommit(c.short, c.subject)).sort());
      // 턴이 없는 커밋은 어느 턴에도 없다.
      for (const c of items.filter((x) => x.turn_key === undefined)) expect(listed, f.id).not.toContain(keyOfCommit(c.short, c.subject));
    }
  });

  it('턴의 work_ids는 턴이 있는 작업 항목의 분할이다', () => {
    for (const { f, a } of fulls) {
      const work = hasContent(a.record) ? a.record.work : [];
      const withTurn = work.filter((w) => w.turn_key !== undefined);
      const listed: string[] = [];
      for (const t of allTurns(a)) {
        expect(t.work_ids, `${f.id}:${t.key}`).toBeDefined();
        const mine = withTurn.filter((w) => w.turn_key === t.key).map((w) => w.id);
        expect([...(t.work_ids ?? [])].sort(), `${f.id}:${t.key}`).toEqual([...mine].sort());
        listed.push(...(t.work_ids ?? []));
      }
      expect([...listed].sort(), f.id).toEqual(withTurn.map((w) => w.id).sort());
    }
  });

  it('턴의 task는 그 턴의 커밋과 작업 항목의 task를 합친 것이다', () => {
    for (const { f, a } of fulls) {
      const work = hasContent(a.record) ? a.record.work : [];
      for (const t of a.turns) {
        const expected = new Set([...(t.commits ?? []).flatMap((c) => c.tasks), ...work.filter((w) => w.turn_key === t.key).flatMap((w) => w.tasks)]);
        expect(new Set(t.tasks), `${f.id}:${t.key}`).toEqual(expected);
      }
    }
  });

  it('커밋이 관측되지 않은 세션은 목록이 비었고 합계가 null이다(0이 아니다)', () => {
    for (const { f, a } of fulls) {
      if (a.commits?.state === 'observed') continue;
      expect(a.commits?.items, f.id).toEqual([]);
      expect(a.commits?.total, f.id).toBeNull();
    }
  });
});

describe('타임라인: 입력 번호 n/of', () => {
  const usersOf = (m: readonly TimelineMessage[]): TimelineMessage[] => m.filter((x) => x.role === 'user');

  it('분석이 없거나 응답이 완전하지 않은 세션과 턴 경계를 모르는 세션은 타임라인이 비었다', () => {
    for (const f of DETAIL_SESSIONS) {
      if (f.analysis === null || !isFullAnalysis(f.analysis) || f.analysis.header.counts.inputs === null) {
        expect(f.timeline, f.id).toEqual([]);
      }
    }
  });

  it('사용자 입력의 번호는 1부터 이어지고, of는 입력 전체 수와 같다', () => {
    for (const { f, a } of fulls) {
      const counts = a.header.counts.inputs;
      if (counts === null) continue;
      const users = usersOf(f.timeline);
      expect(users.map((m) => m.n), f.id).toEqual(users.map((_, i) => i + 1));
      for (const m of users) expect(m.of, `${f.id}:${m.id}`).toBe(counts);
      // 상한에서 잘린 아주 긴 세션만 앞의 창이다. 나머지는 입력 전부다.
      const expectedLength = a.truncated.inputs ? Math.min(a.inputs.length, TIMELINE_WINDOW) : counts;
      expect(users, f.id).toHaveLength(Math.min(expectedLength, counts));
      expect(a.inputs.length, f.id).toBe(a.truncated.inputs ? a.inputs.length : counts);
    }
  });

  it('사용자 입력은 응답의 입력 목록과 같은 번호·출처·턴·시각이다', () => {
    for (const { f, a } of fulls) {
      for (const m of usersOf(f.timeline)) {
        const input = a.inputs[(m.n ?? 0) - 1];
        expect(input, `${f.id}:${m.id}`).toBeDefined();
        expect(m.source, `${f.id}:${m.id}`).toBe(input.kind);
        expect(m.turn_key, `${f.id}:${m.id}`).toBe(input.turn_key);
        expect(m.ts, `${f.id}:${m.id}`).toBe(input.ts);
      }
    }
  });

  it('메시지는 시각 순이고 모든 메시지의 턴이 응답 안에 있다', () => {
    for (const { f, a } of fulls) {
      const keys = new Set(allTurns(a).map((t) => t.key));
      f.timeline.forEach((m, i) => {
        if (i > 0) expect(Date.parse(m.ts), `${f.id}:${m.id}`).toBeGreaterThanOrEqual(Date.parse(f.timeline[i - 1].ts));
        expect(keys.has(m.turn_key), `${f.id}:${m.id}`).toBe(true);
      });
    }
  });

  it('번호는 사용자 입력에만 붙고, id는 유일하다', () => {
    for (const f of DETAIL_SESSIONS) {
      for (const m of f.timeline) {
        if (m.role !== 'user') expect(m.n, `${f.id}:${m.id}`).toBeUndefined();
        if (m.n !== undefined) expect(m.of, `${f.id}:${m.id}`).toBeDefined();
      }
      expect(new Set(f.timeline.map((m) => m.id)).size, f.id).toBe(f.timeline.length);
    }
  });

  it('답한 질문 카드는 답 입력에만, 답하지 않았거나 거절된 카드는 assistant 메시지에만 붙는다', () => {
    let answered = 0;
    let open = 0;
    for (const f of DETAIL_SESSIONS) {
      for (const m of f.timeline) {
        if (!m.question) continue;
        if (m.question.status === 'answered') {
          answered += 1;
          expect(m.role, `${f.id}:${m.id}`).toBe('user');
          expect(m.source, `${f.id}:${m.id}`).toBe('answer');
        } else {
          open += 1;
          expect(m.role, `${f.id}:${m.id}`).toBe('assistant');
        }
      }
    }
    expect(answered).toBeGreaterThan(0);
    expect(open).toBeGreaterThan(0);
  });

  it('답 입력마다 질문 카드가 하나 붙고, 응답의 질문과 같은 객체다', () => {
    for (const { f, a } of fulls) {
      const answerInputs = a.inputs.filter((i) => i.kind === 'answer');
      const answeredQuestions = a.questions.filter((q) => q.status === 'answered');
      expect(answeredQuestions.length, f.id).toBeGreaterThanOrEqual(answerInputs.length);
      for (const m of f.timeline.filter((x) => x.source === 'answer')) {
        expect(m.question, `${f.id}:${m.id}`).toBeDefined();
        expect(a.questions, `${f.id}:${m.id}`).toContainEqual(m.question);
      }
    }
  });

  it('plan 카드는 응답의 플랜과 같다', () => {
    for (const { f, a } of fulls) {
      for (const m of f.timeline.filter((x) => x.plan)) expect(a.plans, `${f.id}:${m.id}`).toContainEqual(m.plan);
    }
  });
});

describe('대표 세션은 레퍼런스의 밀도를 가진다', () => {
  const { f, a } = byId(FLAGSHIP_ID);
  const record = a.record;
  const rich = hasContent(record) ? record : undefined;

  it('요청 14, 입력 15, 턴 14, 버려진 턴 0', () => {
    expect(a.header.counts).toMatchObject({ requests: 14, inputs: 15, turns: 14, abandoned_turns: 0 });
    expect(a.turns).toHaveLength(14);
    expect(a.turns.every((t) => t.is_request && t.status === 'ended')).toBe(true);
    expect(a.inputs).toHaveLength(15);
    expect(a.inputs[1].kind).toBe('answer');
  });

  it('커밋 10건, 변경 파일 16개, PR 하나', () => {
    expect(a.commits).toMatchObject({ state: 'observed', total: 10, truncated: false });
    expect(a.commits?.items).toHaveLength(10);
    expect(new Set(a.commits?.items.map((c) => c.short)).size).toBe(10);
    expect(a.outputs.edited_files.active_total).toBe(16);
    expect(a.outputs.edited_files.items).toHaveLength(16);
    expect(a.outputs.lines).not.toBeNull();
    expect(a.outputs.prs.items).toHaveLength(1);
  });

  it('작업 항목 17개: 상담 2, 의뢰 1, 추가 의뢰 12, 마무리 2. 결정·메모·하위 항목이 있다', () => {
    expect(rich?.work).toHaveLength(17);
    const count = (kind: string): number => rich?.work.filter((w) => w.kind === kind).length ?? -1;
    expect([count('consultation'), count('request'), count('followup'), count('finishing')]).toEqual([2, 1, 12, 2]);
    expect(rich?.work.filter((w) => w.kind === 'consultation').every((w) => (w.decision ?? '').length > 0)).toBe(true);
    expect(rich?.work.some((w) => (w.note ?? '').length > 0)).toBe(true);
    const children = rich?.work.flatMap((w) => w.children ?? []) ?? [];
    expect(children.map((c) => c.kind).sort()).toEqual(['check', 'check', 'work', 'work']);
    expect(rich?.work.every((w) => w.done === true)).toBe(true);
  });

  it('검토 사항은 세 종류가 모두 있고, 태그는 모든 묶음이 있다', () => {
    expect(new Set(rich?.review.map((r) => r.kind))).toEqual(new Set(['decision', 'interpretation', 'unverified']));
    expect(rich?.tags.kinds.length).toBeGreaterThan(0);
    expect(rich?.tags.components.length).toBeGreaterThan(0);
    expect(rich?.tags.screens.length).toBeGreaterThan(0);
    expect(new Set(rich?.tags.related.map((r) => r.kind))).toEqual(new Set(['task', 'feature', 'issue', 'pr']));
    expect(a.header.tags.rules_files.some((r) => r.path === 'AGENTS.md')).toBe(true);
    expect(rich?.bullets.some((b) => b.commits.length > 0)).toBe(true);
    expect(rich?.bullets.some((b) => b.text.includes('`'))).toBe(true);
  });

  it('task-123은 동작 아이콘 넷이 모두 켜져 있고, 켜짐이 다른 task도 있다', () => {
    const task = a.tasks?.items.find((t) => t.id === 'task-123');
    expect(task?.actions).toEqual({ added: true, completed: true, edited: true, commit_linked: true });
    expect(a.tasks?.items.some((t) => Object.values(t.actions).includes(false))).toBe(true);
  });

  it('질문 카드 둘(한 번의 호출, 항목 둘), 플랜 하나', () => {
    expect(a.questions).toHaveLength(1);
    expect(a.questions[0].items).toHaveLength(2);
    expect(a.questions[0].items.every((i) => !i.multi_select && i.options.some((o) => o.recommended))).toBe(true);
    expect(a.plans).toHaveLength(1);
    expect(a.plans[0].decision).toBe('approved');
  });

  it('총 토큰은 26.3M, 비용은 약 $7.57(OTEL), 컨텍스트는 약 30%', () => {
    const total = totalOf(a.header.tokens);
    expect(total).toBeGreaterThanOrEqual(26_250_000);
    expect(total).toBeLessThan(26_350_000);
    expect(a.header.cost.usd).toBeCloseTo(7.57, 2);
    expect(a.header.cost.source).toBe('otel');
    expect(a.header.context.percent).toBeCloseTo(30, 0);
  });

  it('판정은 백그라운드 수집 전 신호를 완료로 취급하지 않는다', () => {
    expect(a.judgement?.checks.map((c) => c.state)).toEqual(['pass', 'pass', 'undeterminable', 'pass', 'pass']);
    expect(a.judgement?.checks[2].reason).toBe('pre_collect');
    expect(a.judgement?.status).toBe('running');
    expect(a.judgement?.bucket).toBeNull();
    expect(a.header.category?.key).toBe('idle');
  });

  it('타임라인: 입력 15, 질문 카드가 붙은 답 입력, 플랜 카드, 중간 출력, 시스템 줄', () => {
    const users = f.timeline.filter((m) => m.role === 'user');
    expect(users).toHaveLength(15);
    expect(users.map((m) => m.n)).toEqual(Array.from({ length: 15 }, (_, i) => i + 1));
    expect(users[1].source).toBe('answer');
    expect(users[1].question?.items).toHaveLength(2);
    expect(users[0].text).toContain('\n\n');
    expect(users[0].images).toBe(1);
    expect(f.timeline.filter((m) => m.plan)).toHaveLength(1);
    expect(f.timeline.filter((m) => m.role === 'assistant' && m.intermediate).length).toBeGreaterThanOrEqual(3);
    expect(f.timeline.filter((m) => m.role === 'assistant' && !m.intermediate).length).toBeGreaterThanOrEqual(14);
    expect(f.timeline.some((m) => m.role === 'system')).toBe(true);
  });

  it('대표 미리보기 PID는 합성값이고 출처 미상 입출력량은 0으로 채우지 않는다', () => {
    const process = a.live?.instances[0].process;
    expect(process?.pid).toBe(18427);
    expect(process?.io_source).toBe('unknown');
    expect(process?.input_lines).toBeNull();
    expect(process?.output_lines).toBeNull();
    expect(process?.input_bytes).toBeNull();
    expect(process?.output_bytes).toBeNull();
  });

  it('메모가 있다', () => {
    expect(a.note?.text.split('\n').length).toBeGreaterThan(1);
    expect(f.row.analysis?.classification).toBe('working');
  });
});

describe('픽스처는 모든 상태를 한 번 이상 보인다', () => {
  const owner = fulls.flatMap(({ a }) => (a.judgement ? [a.judgement] : []));

  it('판정의 상태 여섯: 실행 중 · 완료 · 작업 도중 · 중단 중 · 도중 종료 · 판정 불가', () => {
    const statuses = new Set(owner.map((j) => j.status));
    for (const s of ['running', 'done', 'leftover', 'interrupted', 'ended_midway', 'unknown'] as const) expect(statuses.has(s), s).toBe(true);
  });

  it('판정 불가 상태에는 실패가 없고 다섯 판정 중 하나 이상이 판단 불가다', () => {
    for (const j of owner) {
      if (j.status !== 'unknown') continue;
      expect(j.checks.some((c) => c.state === 'fail')).toBe(false);
      expect(j.checks.some((c) => c.state === 'undeterminable')).toBe(true);
    }
  });

  it('버킷: 완료 · 하다 만 것 · 도중 종료, 그리고 판정을 못 한 null', () => {
    const buckets = new Set(owner.map((j) => j.bucket));
    for (const b of ['done', 'leftover', 'ended_midway', null] as const) expect(buckets.has(b), String(b)).toBe(true);
    // null은 실패는 없지만 완료라고 확인하지 못한 판정이 남았을 때다.
    for (const j of owner) {
      if (j.bucket === null) {
        expect(j.checks.some((c) => c.state === 'undeterminable')).toBe(true);
        expect(j.checks.some((c) => c.state === 'fail')).toBe(false);
      }
      if (j.bucket === 'done') expect(j.checks.every((c) => c.state === 'pass')).toBe(true);
    }
  });

  it('판단 불가의 이유: pre_collect · redacted · no_record · not_applicable', () => {
    const reasons = new Set(owner.flatMap((j) => j.checks.flatMap((c) => (c.reason ? [c.reason] : []))));
    for (const r of ['pre_collect', 'redacted', 'no_record', 'not_applicable'] as const) expect(reasons.has(r), r).toBe(true);
  });

  it('판정 칩의 조합: 전부 통과, 커밋됨·작업 내용 실패, 백그라운드 판단 불가, 기록 없음', () => {
    const states = (id: string): string[] => byId(id).a.judgement?.checks.map((c) => c.state) ?? [];
    expect(states(ALL_PASS_ID)).toEqual(['pass', 'pass', 'pass', 'pass', 'pass']);
    expect(states(LEFTOVER_ID)).toEqual(['pass', 'pass', 'pass', 'fail', 'fail']);
    expect(byId(LEFTOVER_ID).a.judgement?.checks[3].count).toBe(3);
    expect(byId(LEFTOVER_ID).a.judgement?.checks[4].count).toBe(2);
    expect(states(PROCESSING_ID)[2]).toBe('undeterminable');
    expect(states(NO_RECORD_ID)).toEqual(['pass', 'pass', 'pass', 'pass', 'undeterminable']);
    expect(byId(NO_RECORD_ID).a.judgement?.checks[4].reason).toBe('no_record');
  });

  it('세션 기록의 상태 일곱', () => {
    const states = new Set<RecordState>(fulls.flatMap(({ a }) => (a.record ? [a.record.state] : [])));
    for (const s of ['ready', 'stale', 'generating', 'none', 'withheld', 'redacted', 'failed'] as const) expect(states.has(s), s).toBe(true);
    expect(byId(RECORD_STALE_ID).a.record).toMatchObject({ state: 'stale' });
    expect(byId(RECORD_GENERATING_ID).a.record?.state).toBe('generating');
    expect(byId(RECORD_WITHHELD_ID).a.record?.state).toBe('withheld');
    expect(byId(RECORD_REDACTED_ID).a.record?.state).toBe('redacted');
    expect(byId(RECORD_FAILED_ID).a.record?.state).toBe('failed');
  });

  it('낡은 기록은 마지막 활동보다 앞까지만 반영한다', () => {
    const { a } = byId(RECORD_STALE_ID);
    const record = a.record;
    expect(hasContent(record)).toBe(true);
    if (!hasContent(record)) return;
    expect(Date.parse(record.covers_until ?? '')).toBeLessThan(Date.parse(a.header.last_activity_at ?? '') - 10 * 60_000);
  });

  it('비식별화된 세션: 제목과 일부 턴의 첫 줄이 가려졌다', () => {
    const { a } = byId(RECORD_REDACTED_ID);
    expect(a.header.title).toMatchObject({ text: null, state: 'redacted' });
    expect(a.turns.filter((t) => t.preview.state === 'redacted').length).toBeGreaterThanOrEqual(1);
    expect(a.turns.filter((t) => t.preview.state === 'ok').length).toBeGreaterThanOrEqual(1);
  });

  it('처리 중·쉬는 중 세션은 실시간 인스턴스를 가진다', () => {
    expect(byId(PROCESSING_ID).a.live?.instances[0].status).toBe('busy');
    expect(byId(INTERRUPTED_ID).a.live?.instances[0].status).toBe('idle');
    expect(byId(FLAGSHIP_ID).a.live?.instances[0].status).toBe('idle');
    for (const id of [ALL_PASS_ID, LEFTOVER_ID, ENDED_MIDWAY_ID]) expect(byId(id).a.live, id).toBeUndefined();
  });

  it('본문이 없는 기록은 본문 필드가 없다', () => {
    for (const { f, a } of fulls) {
      if (hasContent(a.record)) continue;
      expect(Object.keys(a.record ?? {}).sort(), f.id).toEqual(['covers_until', 'generated_at', 'state']);
    }
  });

  it('커밋의 세 상태, 관측한 0건, 해시를 모르는 커밋', () => {
    const commits = fulls.flatMap(({ a }) => (a.commits ? [a.commits] : []));
    expect(new Set(commits.map((c) => c.state))).toEqual(new Set(['observed', 'undeterminable', 'not_collected']));
    expect(commits.some((c) => c.state === 'observed' && c.total === 0 && c.items.length === 0)).toBe(true);
    const unknown = byId(LEFTOVER_ID).a.commits?.items.filter((c) => c.hash === null && c.short === null);
    expect(unknown).toHaveLength(1);
    expect(unknown?.[0].turn_key).toBeDefined();
  });

  it('빈 task 목록, task 종류, 여러 동작 조합', () => {
    expect(byId(ALL_PASS_ID).a.tasks?.items).toEqual([]);
    const kinds = new Set(fulls.flatMap(({ a }) => (a.tasks?.items ?? []).map((t) => t.kind)));
    expect(kinds.has('task')).toBe(true);
    expect(kinds.has('issue')).toBe(true);
  });

  it('Codex 세션: 턴 신호만 있고 나머지 판정은 해당 없음, 커밋은 수집 안 함', () => {
    const { a } = byId(CODEX_ID);
    expect(a.header.agent).toBe('codex');
    expect(Object.keys(a.signals ?? {})).toEqual(['turn']);
    expect(a.judgement?.checks.map((c) => c.reason ?? c.state)).toEqual(['pass', 'not_applicable', 'not_applicable', 'not_applicable', 'pass']);
    expect(a.commits?.state).toBe('not_collected');
    expect(a.header.cost.source).toBe('estimate');
  });

  it('중단·도중 종료 세션의 타임라인에 안내 줄이 있다', () => {
    const interruptedTimeline = byId(INTERRUPTED_ID).f.timeline;
    const last = interruptedTimeline[interruptedTimeline.length - 1];
    expect(last).toMatchObject({ role: 'system', text: '사용자가 이 턴을 중단했습니다.' });
    expect(byId(ENDED_MIDWAY_ID).f.timeline.some((m) => m.role === 'system' && (m.text ?? '').includes('API 오류'))).toBe(true);
    const rejected = byId(INTERRUPTED_ID).f.timeline.find((m) => m.question?.status === 'rejected');
    expect(rejected?.role).toBe('assistant');
  });

  it('질문 카드의 모양: 여러 개 선택, 자유 응답, 두 개 이상 고른 답', () => {
    const questions = fulls.flatMap(({ a }) => a.questions);
    expect(questions.some((q) => q.items.some((i) => i.multi_select && (i.answer?.labels.length ?? 0) >= 2))).toBe(true);
    expect(questions.some((q) => q.items.some((i) => i.answer?.free_text && i.answer.labels.length === 0))).toBe(true);
    expect(questions.some((q) => q.status === 'rejected')).toBe(true);
  });

  it('패스 A의 상태: ready · stale · building · none · truncated · unsupported · 404', () => {
    const passA = DETAIL_SESSIONS.filter((f) => PREVIEW_SESSIONS.some((p) => p.id === f.id));
    const states = new Set(passA.flatMap((f) => (f.analysis ? [f.analysis.state] : [])));
    for (const s of ['ready', 'stale', 'building', 'none', 'truncated', 'unsupported'] as const) expect(states.has(s), s).toBe(true);
    expect(passA.some((f) => f.analysis === null)).toBe(true);
  });

  it('관측 범주와 분류: 모든 분류 값이 쓰였다', () => {
    const classes = new Set(DETAIL_SESSIONS.flatMap((f) => (f.row.analysis?.classification ? [f.row.analysis.classification] : [])));
    expect(classes).toEqual(new Set(['unsorted', 'consulting', 'working', 'later', 'done']));
  });

  it('응답이 완전하지 않은 상태(building 등)에는 판정이 없다', () => {
    for (const f of DETAIL_SESSIONS) {
      if (f.analysis && !isFullAnalysis(f.analysis)) expect(f.row.analysis?.judgement, f.id).toBeUndefined();
    }
  });
});

describe('목록 묶음의 규칙', () => {
  const rows = DETAIL_SESSIONS.map((f) => rowFor(f, 'owner'));
  const category = (r: (typeof rows)[number]): string | undefined => r.analysis?.category;
  const alive = rows.filter((r) => isAliveCategory(r.analysis?.category));

  it('실행 중인 세션: 처리 중·쉬는 중·중단 중이 있고 백그라운드를 모르면 완료 버킷이 아니다', () => {
    const processing = alive.filter((r) => category(r) === 'running');
    const interrupted = alive.filter((r) => r.analysis?.judgement?.status === 'interrupted');
    const idle = alive.filter((r) => category(r) === 'idle' && r.analysis?.judgement?.status === 'running');
    expect(processing.map((r) => r.session_id)).toContain(PROCESSING_ID);
    expect(interrupted.map((r) => r.session_id)).toEqual([INTERRUPTED_ID]);
    expect(idle.map((r) => r.session_id)).toContain(FLAGSHIP_ID);
    expect(rows.find((r) => r.session_id === FLAGSHIP_ID)?.analysis?.judgement?.bucket).toBeNull();
    // 살아 있는 세션마다 판정이 있다.
    for (const r of alive) expect(r.analysis?.judgement, r.session_id).toBeDefined();
  });

  it('함께 끝난 묶음: 범주 ended의 세 세션이 같은 분 안에 끝났고 다른 ended 세션은 60초 밖이다', () => {
    const ended = rows.filter((r) => category(r) === 'ended');
    const trio = [ALL_PASS_ID, LEFTOVER_ID, ENDED_MIDWAY_ID];
    for (const id of trio) expect(ended.map((r) => r.session_id), id).toContain(id);
    const ends = trio.map((id) => Date.parse(rows.find((r) => r.session_id === id)?.end_time ?? ''));
    const minute = (ms: number): number => Math.floor(ms / 60_000);
    expect(new Set(ends.map(minute)).size).toBe(1);
    expect(Math.max(...ends) - Math.min(...ends)).toBeLessThan(60_000);
    expect(DETAIL_NOW - Math.max(...ends)).toBeGreaterThan(80 * 60_000);
    expect(DETAIL_NOW - Math.max(...ends)).toBeLessThan(110 * 60_000);
    for (const r of ended) {
      if (trio.includes(r.session_id)) continue;
      const distance = Math.min(...ends.map((e) => Math.abs(Date.parse(r.end_time) - e)));
      expect(distance, r.session_id).toBeGreaterThan(60_000);
    }
  });

  it('묶음을 이루는 세 세션의 행은 범주가 ended이고 응답의 범주와 같다', () => {
    for (const id of [ALL_PASS_ID, LEFTOVER_ID, ENDED_MIDWAY_ID]) {
      const { f, a } = byId(id);
      expect(f.row.analysis?.category).toBe('ended');
      expect(a.header.category?.key).toBe('ended');
      expect(a.judgement?.status).not.toBe('running');
    }
  });

  it('살아 있지 않은 나머지 변형은 recent 또는 inactive다', () => {
    const others = [NO_RECORD_ID, RECORD_STALE_ID, RECORD_WITHHELD_ID, RECORD_REDACTED_ID, RECORD_FAILED_ID, CODEX_ID];
    for (const id of others) expect(['recent', 'inactive'], id).toContain(byId(id).f.row.analysis?.category);
  });

  it('범주와 판정의 상태가 어긋나지 않는다: 살아 있으면 실행 중·중단 중, 아니면 그 밖', () => {
    for (const { f, a } of fulls) {
      const aliveNow = isAliveCategory(a.header.category?.key);
      const status = a.judgement?.status;
      if (aliveNow) expect(['running', 'interrupted'], f.id).toContain(status);
      else expect(['running', 'interrupted'], f.id).not.toContain(status);
    }
  });
});

describe('비소유 관리자 화면', () => {
  const OWNER_ONLY = ['signals', 'live', 'judgement', 'note', 'classification'];

  it('집계와 개인 값만 빠지고 내용(기록, 커밋, task, 타임라인)은 그대로다', () => {
    for (const f of DETAIL_SESSIONS) {
      const snapshot = JSON.stringify(f);
      const admin = analysisFor(f, 'admin');
      const row = rowFor(f, 'admin');
      const timeline = timelineFor(f, 'admin');
      expect(JSON.stringify(f), `${f.id}: 원본이 바뀌었다`).toBe(snapshot);
      expect(timeline, f.id).toEqual(f.timeline);

      if (f.row.analysis) {
        const removed = Object.keys(f.row.analysis).filter((k) => !(k in (row.analysis ?? {})));
        const expected = ['category', 'signals', 'judgement', 'note', 'classification'].filter((k) => k in (f.row.analysis ?? {}));
        expect(new Set(removed), `${f.id}:행`).toEqual(new Set(expected));
        expect(row.analysis?.headline, f.id).toEqual(f.row.analysis.headline);
        expect(row.analysis?.commits, f.id).toEqual(f.row.analysis.commits);
      }

      const owner = analysisFor(f, 'owner');
      if (owner === null) {
        expect(admin, f.id).toBeNull();
        continue;
      }
      expect(admin?.owner_view, f.id).toBe(false);
      expect(admin?.identities, f.id).toEqual(f.identities);
      expect(admin?.header && 'category' in admin.header, f.id).toBeFalsy();
      if (isFullAnalysis(owner) && admin && isFullAnalysis(admin)) {
        const removed = Object.keys(owner).filter((k) => !(k in admin));
        expect(new Set(removed), f.id).toEqual(new Set(OWNER_ONLY.filter((k) => k in owner)));
        expect(admin.record, f.id).toEqual(owner.record);
        expect(admin.commits, f.id).toEqual(owner.commits);
        expect(admin.tasks, f.id).toEqual(owner.tasks);
        expect(admin.turns, f.id).toEqual(owner.turns);
        expect(admin.outputs, f.id).toEqual(owner.outputs);
        expect(admin.questions, f.id).toEqual(owner.questions);
      }
    }
  });

  it('주인 화면은 원본과 같은 객체다', () => {
    for (const f of DETAIL_SESSIONS) {
      expect(rowFor(f, 'owner')).toBe(f.row);
      expect(analysisFor(f, 'owner')).toBe(f.analysis);
    }
  });
});

describe('합성 데이터만 쓴다', () => {
  const EVERYTHING = DETAIL_SESSIONS.map((f) => JSON.stringify(f)).join('\n') + JSON.stringify(frameFor('서브')) + SEED_GLOBAL_NOTE;

  it('이메일은 모두 example.com 이다', () => {
    const emails = EVERYTHING.match(/[\w.+-]+@[\w-]+(?:\.[\w-]+)+/g) ?? [];
    expect(emails.length).toBeGreaterThan(10);
    for (const e of emails) expect(e.endsWith('example.com'), e).toBe(true);
  });

  it('URL의 호스트는 모두 example.com 아래다', () => {
    const urls = EVERYTHING.match(/https?:\/\/[^\s"'\\)]+/g) ?? [];
    expect(urls.length).toBeGreaterThan(3);
    for (const u of urls) {
      const host = new URL(u).hostname;
      expect(host === 'example.com' || host.endsWith('.example.com'), u).toBe(true);
    }
  });

  it('숫자는 모두 유한하고 글자에 "undefined"나 "NaN"이 새지 않는다', () => {
    for (const f of DETAIL_SESSIONS) {
      walk(f, f.id.slice(0, 8), (value, path) => {
        if (typeof value === 'number') expect(Number.isFinite(value), path).toBe(true);
        if (typeof value === 'string') {
          expect(value, path).not.toMatch(/undefined|NaN|\[object Object\]|Infinity/);
        }
      });
    }
    walk(frameFor('메인'), 'frame', (value, path) => {
      if (typeof value === 'number') expect(Number.isFinite(value), path).toBe(true);
    });
  });

  it('프레임은 라벨 셋을 모두 쓴다', () => {
    for (const label of FRAME_ACCOUNT_LABELS) {
      expect(frameFor(label).accounts.map((a) => a.label)).toEqual(FRAME_ACCOUNT_LABELS);
    }
  });
});

describe('메모', () => {
  it('메모가 있는 세션마다 전문이 있고 첫 줄이 행의 note와 같다', () => {
    const withNote = DETAIL_SESSIONS.filter((f) => f.row.analysis?.note);
    expect(withNote.length).toBeGreaterThanOrEqual(3);
    for (const f of withNote) {
      const text = SEED_NOTES[f.id];
      expect(text, f.id).toBeDefined();
      expect(text.split('\n')[0].trim(), f.id).toBe(f.row.analysis?.note);
    }
  });

  it('SEED_NOTES에는 메모가 있는 세션만 있고, 전체 메모가 있다', () => {
    for (const id of Object.keys(SEED_NOTES)) {
      expect(DETAIL_SESSIONS.find((f) => f.id === id)?.row.analysis?.note, id).toBeTruthy();
    }
    expect(Object.keys(SEED_NOTES)).toContain(FLAGSHIP_ID);
    expect(SEED_GLOBAL_NOTE.length).toBeGreaterThan(20);
  });

  it('행의 메모와 응답의 메모가 같은 세션이다', () => {
    for (const { f, a } of fulls) {
      if (a.note) expect(f.row.analysis?.note, f.id).toBe(a.note.text.split('\n')[0].trim());
      else expect(f.row.analysis?.note, f.id).toBeNull();
    }
  });
});
