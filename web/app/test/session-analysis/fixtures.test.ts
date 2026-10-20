import { describe, expect, it } from 'vitest';
import { isFullAnalysis } from '@/components/sessions/analysis/guards';
import type { EditedFileItem, SessionAnalysis, SessionAnalysisFull } from '@/lib/session-analysis-types';
import { PREVIEW_NOW, sumAttachedUsd, sumTokens, totalOf } from './builders';
import { PREVIEW_SESSIONS, analysisFor, categoryCountsOf, rowFor, type ViewerRole } from './fixtures';

const fullOnes = (): SessionAnalysisFull[] =>
  PREVIEW_SESSIONS.flatMap((f) => (f.analysis !== null && isFullAnalysis(f.analysis) ? [f.analysis] : []));

/** 배열이 응답 상한에서 잘렸거나 턴 경계를 판단할 수 없는 세션은 배열 합이 전체 합과 같지 않다. */
const sumsComparable = (a: SessionAnalysisFull): boolean =>
  !a.truncated.turns && !a.truncated.abandoned && a.header.counts.turn_boundaries.state === 'observed';

describe('픽스처는 계약의 합계 불변식을 지킨다 (p1b-design §4.4)', () => {
  it('세션 토큰 = Σ턴 토큰(버려진 분기 포함) + 귀속하지 못한 서브에이전트 토큰', () => {
    for (const a of fullOnes().filter(sumsComparable)) {
      const all = [...a.turns, ...a.abandoned];
      expect(totalOf(a.header.tokens), a.session_id).toBe(totalOf(sumTokens(all)) + a.subagents.unattributed_tokens);
    }
  });

  it('세션 비용 = 턴에 붙은 비용 + 턴에 붙지 않은 호출', () => {
    for (const a of fullOnes().filter(sumsComparable)) {
      const { cost } = a.header;
      if (cost.source === 'none') continue;
      const attached = sumAttachedUsd([...a.turns, ...a.abandoned]);
      expect(cost.turn_attached_usd, a.session_id).toBeCloseTo(attached, 4);
      expect(cost.usd, a.session_id).toBeCloseTo(attached + (cost.unattached_turn_usd ?? 0), 4);
    }
  });

  it('요청·턴·버려진 턴 수는 배열과 맞고, 요청 번호는 1부터 이어진다', () => {
    for (const a of fullOnes().filter(sumsComparable)) {
      const requests = a.turns.filter((t) => t.is_request);
      expect(a.header.counts.requests, a.session_id).toBe(requests.length);
      expect(a.header.counts.turns, a.session_id).toBe(a.turns.length);
      expect(a.header.counts.abandoned_turns, a.session_id).toBe(a.abandoned.length);
      expect(
        requests.map((t) => t.request_no),
        a.session_id,
      ).toEqual(requests.map((_, i) => i + 1));
      for (const t of a.turns) expect(t.branch).toBe('active');
      for (const t of a.abandoned) {
        expect(t.branch).toBe('abandoned');
        expect(t.request_no).toBeNull();
      }
    }
  });

  it('턴 키는 세션 안에서 유일하다', () => {
    for (const a of fullOnes()) {
      const keys = [...a.turns, ...a.abandoned].map((t) => t.key);
      expect(new Set(keys).size, a.session_id).toBe(keys.length);
    }
  });

  it('컨텍스트 구성의 합은 컨텍스트 토큰과 같다', () => {
    for (const a of fullOnes()) {
      const { mix, tokens } = a.header.context;
      if (!mix) continue;
      expect(mix.input + mix.cache_read + mix.cache_create, a.session_id).toBe(tokens);
    }
  });

  it('턴 경계를 판단할 수 없는 세션은 수 셋이 null이고 배열이 비었다', () => {
    const undeterminable = fullOnes().filter((a) => a.header.counts.turn_boundaries.state === 'undeterminable');
    expect(undeterminable.length).toBeGreaterThan(0);
    for (const a of undeterminable) {
      expect(a.header.counts.requests).toBeNull();
      expect(a.header.counts.inputs).toBeNull();
      expect(a.header.counts.turns).toBeNull();
      expect(a.turns).toEqual([]);
      expect(a.abandoned).toEqual([]);
    }
  });

  it('Codex는 턴 신호만 내고 해당 없음 신호의 키가 없다', () => {
    const codex = fullOnes().filter((a) => a.header.agent === 'codex');
    expect(codex.length).toBeGreaterThan(0);
    for (const a of codex) expect(Object.keys(a.signals ?? {})).toEqual(['turn']);
  });
});

/**
 * 계약이 함께 나올 수 없다고 한 조합을 픽스처가 만들지 않는지 본다. 위의 합계 시험은 같은 빌더가 만든 값을 다시
 * 더하는 것이라 계약이 아니라 빌더를 확인한다. 이 시험은 계약 문장(p1b-design)에서 직접 가져온 규칙이다.
 */
describe('픽스처는 계약의 조합 규칙을 지킨다', () => {
  const HOUR_MS = 3_600_000;
  const turnsOf = (a: SessionAnalysisFull) => [...a.turns, ...a.abandoned];

  it('복사된 입력으로 열린 턴은 요청이 아니다 (§3.16)', () => {
    let seen = 0;
    for (const a of fullOnes()) {
      for (const t of turnsOf(a).filter((x) => x.opener_copied)) {
        seen += 1;
        expect(t.is_request, `${a.short_id}:${t.key}`).toBe(false);
        expect(t.request_no, `${a.short_id}:${t.key}`).toBeNull();
      }
    }
    expect(seen).toBeGreaterThan(0);
  });

  it('입력 출처와 요청 여부가 §3.9 표와 맞는다(명령은 실제 응답이 있을 때만 요청)', () => {
    const alwaysRequest = new Set(['typed', 'queued', 'unknown']);
    const neverRequest = new Set(['answer', 'notification', 'peer', 'channel', 'auto', 'scheduled', 'sdk', 'system']);
    for (const a of fullOnes()) {
      for (const t of a.turns) {
        const where = `${a.short_id}:${t.key}(${t.kind})`;
        if (t.opener_copied) continue;
        if (alwaysRequest.has(t.kind)) expect(t.is_request, where).toBe(true);
        else if (neverRequest.has(t.kind)) expect(t.is_request, where).toBe(false);
        else if (t.kind === 'command') {
          const answered = t.api_requests > 0 && t.tokens.output > 0;
          expect(t.is_request, where).toBe(answered);
        }
      }
    }
  });

  it('턴 신호는 마지막 활성 턴의 상태다 (§3.12)', () => {
    for (const a of fullOnes()) {
      const signal = a.signals?.turn;
      const last = a.turns[a.turns.length - 1];
      // 배열이 상한에서 잘린 응답은 마지막 원소가 마지막 턴이 아니다.
      if (!signal || !last || a.truncated.turns) continue;
      if (signal.state === 'observed') expect(signal.value, a.short_id).toBe(last.status);
      else expect(last.status, a.short_id).toBe('undeterminable');
    }
  });

  it('범주의 최근성은 임계값(24시간)과 맞는다 (§8.4)', () => {
    for (const a of fullOnes()) {
      const key = a.header.category?.key;
      const last = a.header.last_activity_at;
      if (!key || !last) continue;
      const age = PREVIEW_NOW - Date.parse(last);
      if (key === 'recent' || key === 'collector_delayed') expect(age, `${a.short_id}:${key}`).toBeLessThan(24 * HOUR_MS);
      if (key === 'inactive') expect(age, `${a.short_id}:${key}`).toBeGreaterThanOrEqual(24 * HOUR_MS);
    }
  });

  it('실시간 인스턴스와 범주가 §8.4와 맞는다', () => {
    for (const a of fullOnes()) {
      const key = a.header.category?.key;
      if (!key || !a.live) continue;
      const alive = a.live.instances.filter((i) => !i.stale);
      const where = `${a.short_id}:${key}`;
      if (key === 'waiting') expect(alive.some((i) => i.status === 'waiting' || i.waiting_kind !== ''), where).toBe(true);
      if (key === 'running') expect(alive.some((i) => i.status === 'busy'), where).toBe(true);
      if (key === 'idle') expect(alive.some((i) => i.status === 'idle' || i.status === 'shell'), where).toBe(true);
      if (key === 'stale_live') {
        expect(a.live.instances.length, where).toBeGreaterThan(0);
        expect(alive.length, where).toBe(0);
      }
      if (key === 'ended' || key === 'recent' || key === 'inactive' || key === 'collector_delayed') {
        expect(alive.some((i) => i.status === 'busy' || i.status === 'waiting' || i.waiting_kind !== ''), where).toBe(false);
      }
      if (key === 'collector_delayed') {
        const beat = a.live.collector.heartbeat_at;
        expect(beat !== null && PREVIEW_NOW - Date.parse(beat) > 3 * 60_000, where).toBe(true);
      }
    }
  });

  it('recap이 없는 이유 too_few_turns는 턴이 3개 미만일 때만이다', () => {
    for (const a of fullOnes()) {
      if (a.recaps.absent_reason === 'too_few_turns') expect(a.turns.length, a.short_id).toBeLessThan(3);
    }
  });

  it('상한에 걸린 목록은 상한만큼 담고 전체 수가 더 크다 (§8.2)', () => {
    const caps = { turns: 2_000, abandoned: 500, inputs: 4_000 } as const;
    let turnsTruncated = 0;
    for (const a of fullOnes()) {
      const counts = a.header.counts;
      if (a.truncated.turns) {
        turnsTruncated += 1;
        expect(a.turns.length, a.short_id).toBe(caps.turns);
        expect(counts.turns, a.short_id).toBeGreaterThan(caps.turns);
      }
      if (a.truncated.abandoned) {
        expect(a.abandoned.length, a.short_id).toBe(caps.abandoned);
        expect(counts.abandoned_turns, a.short_id).toBeGreaterThan(caps.abandoned);
      }
      if (a.truncated.inputs) {
        expect(a.inputs.length, a.short_id).toBe(caps.inputs);
        expect(counts.inputs, a.short_id).toBeGreaterThan(caps.inputs);
      }
      // 입력은 턴마다 하나 이상 있으므로 입력 수가 턴 수보다 적을 수 없다(복사된 입력으로 연 턴은 빼고).
      if (counts.inputs !== null && counts.turns !== null) {
        const copiedOpeners = a.turns.filter((t) => t.opener_copied).length;
        expect(counts.inputs, a.short_id).toBeGreaterThanOrEqual(counts.turns - copiedOpeners);
      }
    }
    expect(turnsTruncated).toBeGreaterThan(0);
  });

  it('합계만 있는 truncated 응답의 토큰은 롤업에서 온다 (§3.1)', () => {
    const truncated = PREVIEW_SESSIONS.flatMap((f) => (f.analysis?.state === 'truncated' ? [f.analysis] : []));
    expect(truncated.length).toBeGreaterThan(0);
    for (const a of truncated) expect(a.header?.tokens?.basis).toBe('rollup');
  });

  it('변경 영역은 활성 편집이 가장 많은 앞 두 단계 폴더다(동률은 사전순) (§3.15)', () => {
    const areaOf = (items: readonly EditedFileItem[]): string | null => {
      const tally = new Map<string, number>();
      for (const item of items) {
        const dirs = item.path.split('/').slice(0, -1);
        const key = dirs.slice(0, 2).join('/');
        if (key) tally.set(key, (tally.get(key) ?? 0) + item.edits);
      }
      let best: string | null = null;
      for (const [key, edits] of tally) {
        if (best === null || edits > (tally.get(best) ?? 0) || (edits === tally.get(best) && key < best)) best = key;
      }
      return best;
    };
    let checked = 0;
    for (const a of fullOnes()) {
      const area = a.header.tags.change_area;
      if (!area || area.state !== 'observed') continue;
      expect(area.value, a.short_id).toBe(areaOf(a.outputs.edited_files.items));
      checked += 1;
    }
    expect(checked).toBeGreaterThan(5);
  });

  it('Codex는 편집을 수집하지 않아 턴의 편집도 변경 영역도 없다', () => {
    for (const a of fullOnes().filter((x) => x.header.agent === 'codex')) {
      for (const t of a.turns) expect(t.edited.count, `${a.short_id}:${t.key}`).toBe(0);
      expect(a.header.tags.change_area?.value ?? null, a.short_id).toBeNull();
    }
  });

  it('제목이 첫 요청이면 첫 요청 턴의 첫 줄과 같다 (§3.18)', () => {
    let checked = 0;
    for (const a of fullOnes()) {
      if (a.header.title.source !== 'first_request') continue;
      const first = a.turns.find((t) => t.is_request);
      expect(first?.preview.text, a.short_id).toBe(a.header.title.text);
      checked += 1;
    }
    expect(checked).toBeGreaterThan(1);
  });

  it('턴이 없는 세션의 비용은 전부 "턴에 붙지 않은 호출"이다 (§4.2)', () => {
    for (const a of fullOnes().filter((x) => x.header.counts.turn_boundaries.state === 'undeterminable')) {
      expect(a.header.cost.turn_attached_usd, a.short_id).toBe(0);
      expect(a.header.cost.unattached_turn_usd, a.short_id).toBe(a.header.cost.usd);
    }
  });

  it('플랜·질문·PR이 가리키는 턴은 응답 안에 있다', () => {
    for (const a of fullOnes()) {
      const keys = new Set(turnsOf(a).map((t) => t.key));
      for (const p of a.plans) expect(keys.has(p.turn_key), `${a.short_id}:plan:${p.turn_key}`).toBe(true);
      for (const q of a.questions) expect(keys.has(q.turn_key), `${a.short_id}:question:${q.turn_key}`).toBe(true);
      for (const pr of a.outputs.prs.items) if (pr.turn_key) expect(keys.has(pr.turn_key), `${a.short_id}:pr:${pr.turn_key}`).toBe(true);
    }
  });
});

describe('픽스처는 계약의 모든 상태를 한 번 이상 보인다', () => {
  const analyses = (): SessionAnalysis[] => PREVIEW_SESSIONS.flatMap((f) => (f.analysis ? [f.analysis] : []));
  const full = fullOnes;
  const turns = () => full().flatMap((a) => [...a.turns, ...a.abandoned]);

  it('응답 state: ready · stale · building · none · truncated · unsupported', () => {
    expect(new Set(analyses().map((a) => a.state))).toEqual(
      new Set(['ready', 'stale', 'building', 'none', 'truncated', 'unsupported']),
    );
  });

  it('목록 행 state: 응답 상태 여섯 + withheld(신원 섞임)', () => {
    const states = new Set(PREVIEW_SESSIONS.map((f) => f.row.analysis?.state));
    expect(states).toEqual(new Set(['ready', 'stale', 'building', 'none', 'truncated', 'unsupported', 'withheld']));
    expect(PREVIEW_SESSIONS.find((f) => f.row.analysis?.state === 'withheld')?.analysis).toBeNull();
  });

  it('관측 범주 아홉 가지', () => {
    const keys = new Set(PREVIEW_SESSIONS.map((f) => f.row.analysis?.category).filter(Boolean));
    expect(keys).toEqual(
      new Set(['waiting', 'running', 'ended', 'stale_live', 'open_question', 'idle', 'collector_delayed', 'recent', 'inactive']),
    );
  });

  it('제목 상태 ok · withheld · redacted · none', () => {
    expect(new Set(full().map((a) => a.header.title.state))).toEqual(new Set(['ok', 'withheld', 'redacted', 'none']));
  });

  it('세션 비용 출처 다섯 가지(none은 롤업 머리에서)', () => {
    const sources = new Set(
      PREVIEW_SESSIONS.flatMap((f) => (f.analysis?.header?.cost?.source ? [f.analysis.header.cost.source] : [])),
    );
    expect(sources).toEqual(new Set(['otel', 'otel_partial', 'estimate', 'estimate_pending', 'none']));
    expect(full().some((a) => a.header.cost.note === 'otel_expired')).toBe(true);
  });

  it('턴 비용 출처 여덟 가지와 귀속 불가의 두 이유', () => {
    expect(new Set(turns().map((t) => t.cost.source))).toEqual(
      new Set(['otel', 'otel_partial', 'expired', 'estimate', 'estimate_pending', 'no_rate', 'unattributable', 'none']),
    );
    expect(new Set(turns().flatMap((t) => (t.cost.reason ? [t.cost.reason] : [])))).toEqual(new Set(['pre_p0', 'shared_prompt_id']));
  });

  it('턴 상태 여섯 가지와 판단 불가의 네 이유', () => {
    expect(new Set(turns().map((t) => t.status))).toEqual(
      new Set(['ended', 'interrupted', 'error', 'no_response', 'open', 'undeterminable']),
    );
    expect(new Set(turns().flatMap((t) => (t.status_reason ? [t.status_reason] : [])))).toEqual(
      new Set(['pre_collect', 'redacted', 'no_lifecycle', 'no_end_line']),
    );
  });

  it('입력 출처: 요청이 아닌 턴이 가진 kind 일곱 가지와 요청의 queued·command', () => {
    const nonRequest = new Set(turns().filter((t) => !t.is_request).map((t) => t.kind));
    for (const kind of ['command', 'notification', 'peer', 'scheduled', 'channel', 'auto', 'sdk', 'system'] as const) {
      expect(nonRequest.has(kind), kind).toBe(true);
    }
    const requestKinds = new Set(turns().filter((t) => t.is_request).map((t) => t.kind));
    expect(requestKinds.has('queued')).toBe(true);
    expect(requestKinds.has('typed')).toBe(true);
  });

  it('첫 줄 상태 ok · redacted · withheld · none', () => {
    expect(new Set(turns().map((t) => t.preview.state))).toEqual(new Set(['ok', 'redacted', 'withheld', 'none']));
  });

  it('플랜 결정 네 가지와 본문 상태 세 가지', () => {
    const plans = full().flatMap((a) => a.plans);
    expect(new Set(plans.map((p) => p.decision))).toEqual(new Set(['approved', 'rejected', 'interrupted', 'pending']));
    expect(new Set(plans.map((p) => p.state))).toEqual(new Set(['observed', 'redacted', 'withheld']));
  });

  it('질문 상태 세 가지(턴의 머리글)', () => {
    const statuses = new Set(turns().flatMap((t) => t.question_headers.map((q) => q.status)));
    expect(statuses).toEqual(new Set(['answered', 'rejected', 'unanswered']));
  });

  it('신호: 판단 불가의 여섯 이유와 관측한 0', () => {
    const reasons = new Set<string>();
    let observedZero = false;
    for (const a of full()) {
      for (const s of Object.values(a.signals ?? {})) {
        if (s.state === 'undeterminable') reasons.add(s.reason);
        else if (s.value === 0) observedZero = true;
      }
    }
    expect(reasons).toEqual(new Set(['pre_collect', 'withheld', 'dropped', 'redacted', 'no_list', 'no_git']));
    expect(observedZero).toBe(true);
  });

  it('recap: 없는 이유 여섯 가지와 있는 경우', () => {
    const reasons = new Set(full().flatMap((a) => (a.recaps.absent_reason ? [a.recaps.absent_reason] : [])));
    expect(reasons).toEqual(new Set(['too_few_turns', 'headless', 'not_away', 'pre_collect', 'withheld', 'redacted']));
    expect(full().some((a) => a.recaps.items.length > 0)).toBe(true);
    expect(full().some((a) => a.recaps.items.some((r) => r.repeats > 1))).toBe(true);
  });

  it('복사: 원본이 보이는 경우, 안 보이는 경우, 판정을 건너뛴 경우', () => {
    const copies = full().flatMap((a) => (a.copies ? [a.copies] : []));
    expect(copies.some((c) => c.state === 'judged' && c.original_visible && c.original_session_id)).toBe(true);
    expect(copies.some((c) => c.state === 'judged' && !c.original_visible && c.original_session_id === null)).toBe(true);
    expect(copies.some((c) => c.state === 'skipped_large_group')).toBe(true);
  });

  it('복사 원본은 픽스처 안의 다른 세션이다', () => {
    const ids = new Set(PREVIEW_SESSIONS.map((f) => f.id));
    for (const a of full()) {
      if (a.copies?.original_session_id) expect(ids.has(a.copies.original_session_id)).toBe(true);
    }
  });

  it('컨텍스트: 관측 · 모델 표기 · 모름, 큰 창 흔적', () => {
    expect(new Set(full().map((a) => a.header.context.source))).toEqual(new Set(['observed', 'model_default', 'unknown']));
    expect(full().some((a) => a.header.context.large_window_seen && a.header.context.percent === null)).toBe(true);
  });

  it('상한에 걸린 목록: 턴 · 버려진 턴 · 편집 파일 · PR · recap · 본문', () => {
    const a = full();
    expect(a.some((x) => x.truncated.turns)).toBe(true);
    expect(a.some((x) => x.truncated.abandoned)).toBe(true);
    expect(a.some((x) => x.truncated.recaps)).toBe(true);
    expect(a.some((x) => x.truncated.bodies)).toBe(true);
    expect(a.some((x) => x.outputs.edited_files.truncated)).toBe(true);
    expect(a.some((x) => x.outputs.prs.truncated)).toBe(true);
  });

  it('편집 파일 redacted, 줄 수 없음(null)과 일부(partial)', () => {
    expect(full().some((a) => a.outputs.edited_files.state === 'redacted')).toBe(true);
    expect(full().some((a) => a.outputs.lines === null)).toBe(true);
    expect(full().some((a) => a.outputs.lines?.partial === true)).toBe(true);
  });

  it('플랜 파일과 플랜 모드는 따로다', () => {
    const flags = full().map((a) => a.header.plan_flags);
    expect(flags.some((f) => f.has_plan_file && !f.used_plan_mode)).toBe(true);
    expect(flags.some((f) => !f.has_plan_file && f.used_plan_mode)).toBe(true);
    expect(flags.some((f) => f.has_plan_file && f.used_plan_mode)).toBe(true);
  });
});

describe('합성 데이터만 쓴다', () => {
  const everyString = (value: unknown, out: string[] = []): string[] => {
    if (typeof value === 'string') out.push(value);
    else if (Array.isArray(value)) for (const v of value) everyString(v, out);
    else if (value && typeof value === 'object') for (const v of Object.values(value)) everyString(v, out);
    return out;
  };

  it('이메일은 모두 example.com 이다', () => {
    const emails = everyString(PREVIEW_SESSIONS).flatMap((s) => s.match(/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g) ?? []);
    expect(emails.length).toBeGreaterThan(10);
    for (const email of emails) expect(email.endsWith('@example.com'), email).toBe(true);
  });

  it('PR 링크는 example.com 아래다', () => {
    const urls = everyString(PREVIEW_SESSIONS).filter((s) => /^https?:\/\//.test(s));
    expect(urls.length).toBeGreaterThan(0);
    for (const url of urls) expect(new URL(url).hostname.endsWith('example.com'), url).toBe(true);
  });

  it('세션 ID는 모두 다르고 앞 8자도 겹치지 않는다(목록과 짧은 ID)', () => {
    const ids = PREVIEW_SESSIONS.map((f) => f.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(new Set(ids.map((id) => id.slice(0, 8))).size).toBe(ids.length);
  });
});

describe('목록 행은 분석 응답에서 나온다', () => {
  it('요청 수·컨텍스트·범주·신호가 응답과 같다', () => {
    for (const f of PREVIEW_SESSIONS) {
      const a = f.analysis;
      const row = f.row.analysis;
      if (!a || !isFullAnalysis(a) || !row) continue;
      expect(row.requests, f.id).toBe(a.header.counts.requests);
      expect(row.context?.percent, f.id).toBe(a.header.context.percent);
      expect(row.category, f.id).toBe(a.header.category?.key);
      for (const [key, signal] of Object.entries(a.signals ?? {})) {
        const value = row.signals?.[key as keyof NonNullable<typeof row.signals>];
        // 세션 중간부터 모은 미커밋의 0(`at_least`)은 목록에서도 판단 불가(null)다.
        const partialZero = 'at_least' in signal && signal.at_least && signal.value === 0;
        expect(value, `${f.id}:${key}`).toBe(signal.state === 'observed' && !partialZero ? signal.value : null);
      }
    }
  });

  it('목록 행의 신호에 해당 없음 키가 없다(Codex)', () => {
    const codex = PREVIEW_SESSIONS.find((f) => f.row.agent === 'codex');
    expect(Object.keys(codex?.row.analysis?.signals ?? {})).toEqual(['turn']);
  });

  it('범주 개수는 주인 범위의 행에서만 센다', () => {
    const owner = categoryCountsOf(PREVIEW_SESSIONS.map((f) => rowFor(f, 'owner')));
    const admin = categoryCountsOf(PREVIEW_SESSIONS.map((f) => rowFor(f, 'admin')));
    expect(Object.values(owner).reduce((s, n) => s + (n ?? 0), 0)).toBeGreaterThanOrEqual(9);
    expect(admin).toEqual({});
  });
});

describe('비소유 관리자 화면', () => {
  const role: ViewerRole = 'admin';

  it('신호·범주·실시간이 없고 신원 정보가 온다', () => {
    for (const f of PREVIEW_SESSIONS) {
      const admin = analysisFor(f, role);
      if (admin === null) continue;
      expect(admin.owner_view, f.id).toBe(false);
      expect(admin.identities, f.id).toBeDefined();
      expect(admin.header?.category, f.id).toBeUndefined();
      if (isFullAnalysis(admin)) {
        expect(admin.signals, f.id).toBeUndefined();
        expect(admin.live, f.id).toBeUndefined();
      }
    }
  });

  it('주인 화면은 그대로다', () => {
    const rich = PREVIEW_SESSIONS[0];
    const owner = analysisFor(rich, 'owner');
    expect(owner).toBe(rich.analysis);
    expect(owner && isFullAnalysis(owner) ? Object.keys(owner.signals ?? {}) : []).toHaveLength(5);
  });

  it('목록 행에서도 범주와 신호가 빠진다', () => {
    for (const f of PREVIEW_SESSIONS) {
      const row = rowFor(f, 'admin');
      expect(row.analysis?.category, f.id).toBeUndefined();
      expect(row.analysis?.signals, f.id).toBeUndefined();
    }
  });

  it('원본 응답을 바꾸지 않는다', () => {
    const rich = PREVIEW_SESSIONS[0];
    analysisFor(rich, 'admin');
    expect(rich.analysis && isFullAnalysis(rich.analysis) ? rich.analysis.signals : null).toBeDefined();
    expect(rich.row.analysis?.category).toBe('waiting');
  });
});
