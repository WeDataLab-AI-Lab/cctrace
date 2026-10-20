import type { AnalysisInput, AnalysisPlan, AnalysisQuestion, SessionAnalysis, SessionAnalysisFull, TimelineMessage } from '@/lib/session-analysis-types';
import type { SessionRecord } from '@/lib/types';

type FreshnessState = 'fresh' | 'stale' | 'partial' | 'unavailable';

interface TimelineFreshness {
  state: FreshnessState;
  detail: string;
  matchedInputs: number;
  expectedInputs: number | null;
  computedAt: string | null;
  rulesVersion: number | null;
}

interface TimelineBuild {
  messages: TimelineMessage[];
  inputCount: number | null;
  freshness: TimelineFreshness;
}

interface TimelineBuildOptions {
  recordsComplete: boolean;
}

const isFullAnalysis = (analysis: SessionAnalysis): analysis is SessionAnalysisFull =>
  analysis.state === 'ready' || analysis.state === 'stale';

const instant = (value: string): number | null => {
  const result = Date.parse(value);
  return Number.isFinite(result) ? result : null;
};

const inputWitnessKey = (uuid: string, ts: string): string => `${uuid}\u0000${instant(ts) ?? ts}`;

const recordTurnKey = (
  record: SessionRecord,
  timestamp: number | null,
  byRootTurn: ReadonlyMap<string, string>,
  byFinalMessage: ReadonlyMap<string, string>,
  turns: SessionAnalysisFull['turns'],
): string => {
  if (record.codex_root_turn_id && byRootTurn.has(record.codex_root_turn_id)) return byRootTurn.get(record.codex_root_turn_id)!;
  if (record.message_id && byFinalMessage.has(record.message_id)) return byFinalMessage.get(record.message_id)!;
  if (record.is_sidechain || timestamp === null) return '';
  const candidates = turns.filter((turn) => {
    const start = instant(turn.started_at);
    const end = instant(turn.ended_at);
    return start !== null && timestamp >= start && (end === null || timestamp <= end);
  });
  candidates.sort((a, b) => (instant(b.started_at) ?? 0) - (instant(a.started_at) ?? 0));
  return candidates[0]?.key ?? '';
};

const emptyFreshness = (detail: string, expectedInputs: number | null = null): TimelineFreshness => ({
  state: 'unavailable', detail, matchedInputs: 0, expectedInputs, computedAt: null, rulesVersion: null,
});

const codexFinalRecordByTurn = (records: readonly SessionRecord[]): Map<string, string> => {
  const rows = new Map<string, Array<{ id: string; ts: number }>>();
  for (const record of records) {
    if (!record.codex_turn_metadata_known || !record.codex_root_turn_id || record.view?.role !== 'assistant' || record.view.kind !== 'message') continue;
    const ts = instant(record.ts);
    if (ts === null) continue;
    const group = rows.get(record.codex_root_turn_id) ?? [];
    group.push({ id: record.view.anchor_id || record.uuid || `${record.session_id}:${record.ts}`, ts });
    rows.set(record.codex_root_turn_id, group);
  }
  const result = new Map<string, string>();
  for (const [turn, group] of rows) {
    const last = Math.max(...group.map((row) => row.ts));
    const finalRows = group.filter((row) => row.ts === last);
    if (finalRows.length === 1) result.set(turn, finalRows[0].id);
  }
  return result;
};

const attachQuestions = (messages: TimelineMessage[], questions: readonly AnalysisQuestion[]): TimelineMessage[] => {
  const result = new Map(messages.map((message) => [message.id, message]));
  const usedAnswers = new Set<string>();
  for (const question of questions) {
    let target: TimelineMessage | undefined;
    if (question.status === 'answered') {
      target = messages
        .filter((message) => message.role === 'user' && message.source === 'answer' && message.turn_key === question.turn_key &&
          instant(message.ts) !== null && instant(question.ts) !== null && instant(message.ts)! >= instant(question.ts)! && !usedAnswers.has(message.id))
        .sort((a, b) => (instant(a.ts) ?? 0) - (instant(b.ts) ?? 0))[0];
      if (target) usedAnswers.add(target.id);
    }
    if (target) {
      result.set(target.id, { ...target, question });
    } else {
      const id = `question:${question.tool_use_id}`;
      result.set(id, { id, ts: question.ts, role: 'assistant', turn_key: question.turn_key, question });
    }
  }
  return [...result.values()];
};

const withCards = (messages: TimelineMessage[], questions: readonly AnalysisQuestion[], plans: readonly AnalysisPlan[]): TimelineMessage[] => {
  const result = attachQuestions(messages, questions);
  for (const [index, plan] of plans.entries()) {
    result.push({
      id: `plan:${plan.turn_key}:${plan.ts}:${index}`,
      ts: plan.ts,
      role: 'assistant',
      turn_key: plan.turn_key,
      plan,
    });
  }
  return result;
};

const timelineOrder = (a: TimelineMessage, b: TimelineMessage): number => {
  const aTime = instant(a.ts) ?? 0;
  const bTime = instant(b.ts) ?? 0;
  return aTime - bTime || a.id.localeCompare(b.id);
};

/**
 * Map owner-scoped P1b source facts onto the normalized records from GET /api/sessions.
 * Only a matching analysis input UUID and timestamp receives an input number/source; an
 * unpaired raw user-role row remains neutral and is never upgraded to a direct human input.
 */
const buildTimeline = (records: readonly SessionRecord[], analysis: SessionAnalysis, options: TimelineBuildOptions = { recordsComplete: true }): TimelineBuild => {
  if (!isFullAnalysis(analysis)) {
    return { messages: rawMessages(records, new Map(), [], new Map(), new Map(), new Map(), null), inputCount: null,
      freshness: emptyFreshness(`분석 입력을 아직 읽을 수 없습니다(${analysis.state}).`) };
  }
  if (!analysis.owner_view) {
    return { messages: rawMessages(records, new Map(), [], new Map(), new Map(), new Map(), null), inputCount: null,
      freshness: emptyFreshness('세션 주인의 입력 출처와 턴 사실은 이 조회 범위에 포함되지 않습니다.') };
  }

  const inputsByWitness = new Map<string, AnalysisInput>();
  for (const input of analysis.inputs) {
    if (!input.uuid || instant(input.ts) === null || !Number.isSafeInteger(input.n) || input.n < 1) continue;
    inputsByWitness.set(inputWitnessKey(input.uuid, input.ts), input);
  }
  const turns = [...analysis.turns, ...analysis.abandoned];
  const byRootTurn = new Map(turns.map((turn) => [turn.key, turn.key]));
  const byFinalMessage = new Map(turns.filter((turn) => turn.last_assistant_message_id)
    .map((turn) => [turn.last_assistant_message_id, turn.key]));
  const fullCount = analysis.header.counts.inputs;
  const inputCount = Number.isSafeInteger(fullCount) && fullCount !== null && fullCount >= 0 ? fullCount : null;
  const codexFinals = codexFinalRecordByTurn(records);
  const messages = rawMessages(records, inputsByWitness, turns, byRootTurn, byFinalMessage, codexFinals, inputCount);
  const matchedInputs = messages.filter((message) => message.role === 'user' && message.n !== undefined).length;
  const missingWitness = !options.recordsComplete || inputCount === null || matchedInputs < Math.min(inputCount, analysis.inputs.length);
  const truncated = analysis.truncated.inputs || analysis.truncated.questions || analysis.truncated.plans || analysis.truncated.bodies;
  const freshness: TimelineFreshness = {
    state: truncated || missingWitness ? 'partial' : analysis.state === 'stale' ? 'stale' : 'fresh',
    detail: truncated
      ? '분석 API 응답이 상한에서 잘렸습니다. 표시되는 입력만 출처 사실과 대조했습니다.'
      : missingWitness
        ? '원문 페이지를 모두 읽지 못했거나 일부 입력에 원문 UUID 증거가 없습니다.'
        : analysis.state === 'stale'
          ? `저장된 분석 규칙 판 ${analysis.rules_version}이 현재 판보다 오래되었습니다.`
          : '입력 출처는 원문 UUID와 세션 분석 사실의 일치로 확인했습니다.',
    matchedInputs,
    expectedInputs: inputCount,
    computedAt: analysis.computed_at,
    rulesVersion: analysis.rules_version,
  };
  return {
    messages: withCards(messages, analysis.questions, analysis.plans).sort(timelineOrder),
    inputCount,
    freshness,
  };
};

const rawMessages = (
  records: readonly SessionRecord[],
  inputs: ReadonlyMap<string, AnalysisInput>,
  turns: SessionAnalysisFull['turns'],
  byRootTurn: ReadonlyMap<string, string>,
  byFinalMessage: ReadonlyMap<string, string>,
  codexFinals: ReadonlyMap<string, string>,
  inputCount: number | null,
): TimelineMessage[] => {
  const turnsByKey = new Map(turns.map((turn) => [turn.key, turn]));
  const messages: TimelineMessage[] = [];
  for (const record of records) {
    const view = record.view;
    if (!view || view.kind === 'hidden' || view.kind === 'reasoning' || view.kind === 'tool_call') continue;
    const ts = record.ts;
    const timestamp = instant(ts);
    const witness = record.uuid ? inputs.get(inputWitnessKey(record.uuid, ts)) : undefined;
    // A tool_result becomes a human answer only on the exact source UUID+timestamp witness.
    // Its normalized tool body stays out of the timeline message.
    const witnessedAnswer = view.kind === 'tool_result' && witness?.kind === 'answer';
    if (view.kind === 'tool_result' && !witnessedAnswer) continue;
    const rawRole = view.role;
    // The API normalizer keeps tool-only assistant rows as messages; this timeline
    // renders their question/plan facts separately and has no visible tool-call body.
    if (rawRole === 'assistant' && !view.text?.trim()) continue;
    const role = witnessedAnswer ? 'user' : rawRole === 'assistant' ? 'assistant' : rawRole === 'user' ? 'user' : 'system';
    const exactCodexTurn = record.codex_turn_metadata_known && record.codex_root_turn_id
      ? byRootTurn.get(record.codex_root_turn_id) : undefined;
    const turnKey = witness?.turn_key ?? exactCodexTurn ?? (role === 'user' ? '' : recordTurnKey(record, timestamp, byRootTurn, byFinalMessage, turns));
    if (role === 'system' && !view.text) continue;
    const turn = turnsByKey.get(turnKey);
    const message: TimelineMessage = {
      id: view.anchor_id || record.uuid || `${record.session_id}:${ts}`,
      ts,
      role,
      turn_key: turnKey,
      ...(role === 'user' ? {
        source: witness?.kind ?? 'unknown',
        source_witness: witness ? 'analysis_input_uuid' : 'unmatched_record',
        ...(witness ? { n: witness.n } : {}),
        ...(witness && inputCount !== null ? { of: inputCount } : {}),
      } : {}),
      ...(role === 'system' && !rawRole && record.record_type !== 'system' ? { source_witness: 'unmatched_record' as const } : {}),
      ...(typeof view.text === 'string' ? { text: view.text } : {}),
      ...(witness && witness.images > 0 ? { images: witness.images } : {}),
      ...(role === 'assistant' && turn?.last_assistant_message_id
        ? { intermediate: !!record.message_id && record.message_id !== turn.last_assistant_message_id }
        : role === 'assistant' && exactCodexTurn && codexFinals.has(exactCodexTurn)
          ? { intermediate: (view.anchor_id || record.uuid || `${record.session_id}:${ts}`) !== codexFinals.get(exactCodexTurn) }
          : {}),
    };
    messages.push(message);
  }
  return messages;
};

export { buildTimeline };
export type { TimelineBuild, TimelineBuildOptions, TimelineFreshness, FreshnessState };
