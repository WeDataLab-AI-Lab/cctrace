import { describe, expect, it } from 'vitest';
import type { AnalysisInput, AnalysisPlan, AnalysisQuestion, SessionAnalysis } from '@/lib/session-analysis-types';
import type { SessionRecord } from '@/lib/types';
import { filterMessages, isInput } from './model';
import { buildTimeline } from './record-adapter';

const stamp = (seconds: number) => new Date(Date.UTC(2026, 9, 9, 10, 0, seconds)).toISOString();

const record = (uuid: string, role: 'user' | 'assistant' | undefined, seconds: number, over: Partial<SessionRecord> = {}): SessionRecord => ({
  ts: stamp(seconds), session_id: 'session', project_hash: 'project', record_type: role ?? 'other', profile_email: '', model: '',
  uuid, message_id: '', tool_name: '', view: { kind: 'message', role, text: `본문 ${uuid}`, anchor_id: uuid, dedupe_key: '' },
  ...over,
});

const input = (uuid: string, n: number, kind: AnalysisInput['kind'], turn_key: string, seconds: number): AnalysisInput => ({
  uuid, ts: stamp(seconds), n, kind, turn_key, images: 0,
});

const baseAnalysis = (over: Record<string, unknown> = {}): SessionAnalysis => ({
  session_id: 'session', short_id: 'session', owner_view: true, state: 'ready', rules_version: 3, computed_at: stamp(10),
  header: { counts: { inputs: 2 } }, turns: [], abandoned: [], inputs: [], questions: [], plans: [],
  truncated: { turns: false, abandoned: false, inputs: false, questions: false, plans: false, recaps: false, bodies: false },
  ...over,
} as unknown as SessionAnalysis);

describe('timeline adapter reads the actual record and analysis APIs', () => {
  it('requires an exact input UUID witness and preserves neutral origins and turn anchors', () => {
    const analysis = baseAnalysis({ inputs: [input('typed', 1, 'typed', 'turn-1', 1), input('command', 2, 'command', 'turn-2', 3)],
      turns: [
        { key: 'turn-1', started_at: stamp(1), ended_at: stamp(2), last_assistant_message_id: 'final-1' },
        { key: 'turn-2', started_at: stamp(3), ended_at: stamp(4), last_assistant_message_id: 'final-2' },
      ] });
    const result = buildTimeline([
      record('typed', 'user', 1),
      record('unmatched-user', 'user', 2),
      record('mid', 'assistant', 2, { message_id: 'middle-1' }),
      record('final', 'assistant', 2, { message_id: 'final-1' }),
      record('command', 'user', 3),
      record('unknown-role', undefined, 4),
    ], analysis);
    const typed = result.messages.find((message) => message.id === 'typed')!;
    const unmatched = result.messages.find((message) => message.id === 'unmatched-user')!;
    const command = result.messages.find((message) => message.id === 'command')!;
    expect(typed).toMatchObject({ role: 'user', source: 'typed', source_witness: 'analysis_input_uuid', n: 1, of: 2, turn_key: 'turn-1' });
    expect(unmatched).toMatchObject({ role: 'user', source: 'unknown', turn_key: '' });
    expect(unmatched.n).toBeUndefined();
    expect(command).toMatchObject({ role: 'user', source: 'command', n: 2, turn_key: 'turn-2' });
    expect(result.messages.find((message) => message.id === 'mid')?.intermediate).toBe(true);
    expect(result.messages.find((message) => message.id === 'final')?.intermediate).toBe(false);
    expect(result.messages.find((message) => message.id === 'unknown-role')?.role).toBe('system');
    expect(result.inputCount).toBe(2);
  });

  it('attaches question answers and plan decisions to API-backed turn cards', () => {
    const question = { tool_use_id: 'question-1', turn_key: 'turn-1', ts: stamp(2), status: 'answered', state: 'observed', items: [] } as AnalysisQuestion;
    const unanswered = { ...question, tool_use_id: 'question-2', turn_key: 'turn-2', ts: stamp(4), status: 'unanswered' } as AnalysisQuestion;
    const plan = { turn_key: 'turn-2', ts: stamp(5), file_path: '/plan.md', decision: 'rejected', body: '계획 본문', state: 'observed' } as AnalysisPlan;
    const result = buildTimeline([record('answer', 'user', 3)], baseAnalysis({
      inputs: [input('answer', 1, 'answer', 'turn-1', 3)], questions: [question, unanswered], plans: [plan],
    }));
    expect(result.messages.find((message) => message.id === 'answer')?.question).toBe(question);
    expect(result.messages.find((message) => message.question?.tool_use_id === 'question-2')).toMatchObject({ role: 'assistant', turn_key: 'turn-2' });
    expect(result.messages.find((message) => message.plan?.decision === 'rejected')).toMatchObject({ role: 'assistant', turn_key: 'turn-2' });
  });

  it('omits normalized tool-only assistant rows while keeping displayable conversation and cards', () => {
    const toolOnly = record('tool-only', 'assistant', 2, {
      view: { kind: 'message', role: 'assistant', tool_calls: [{ id: 'toolu-1', name: 'Read' }], anchor_id: 'tool-only', dedupe_key: 'source-line' },
    });
    const question = { tool_use_id: 'question-1', turn_key: 'turn-1', ts: stamp(2), status: 'unanswered', state: 'observed', items: [] } as AnalysisQuestion;
    const plan = { turn_key: 'turn-1', ts: stamp(3), file_path: '/plan.md', decision: 'pending', body: '계획 본문', state: 'observed' } as AnalysisPlan;
    const result = buildTimeline([
      toolOnly,
      record('assistant-text', 'assistant', 4, { view: { kind: 'message', role: 'assistant', text: '완료했습니다.', anchor_id: 'assistant-text', dedupe_key: 'assistant-text' } }),
      record('image-input', 'user', 5),
      record('unmatched-human', 'user', 6),
    ], baseAnalysis({
      header: { counts: { inputs: 1 } },
      turns: [{ key: 'turn-1', started_at: stamp(1), ended_at: stamp(5), last_assistant_message_id: 'assistant-text' }],
      inputs: [{ ...input('image-input', 1, 'typed', 'turn-1', 5), images: 1 }],
      questions: [question], plans: [plan],
    }));

    expect(result.messages.some((message) => message.id === 'tool-only')).toBe(false);
    expect(result.messages.find((message) => message.id === 'assistant-text')?.text).toBe('완료했습니다.');
    expect(result.messages.find((message) => message.id === 'image-input')?.images).toBe(1);
    expect(result.messages.find((message) => message.id === 'unmatched-human')).toMatchObject({ role: 'user', source: 'unknown' });
    expect(result.messages.some((message) => message.question?.tool_use_id === 'question-1')).toBe(true);
    expect(result.messages.some((message) => message.plan?.file_path === '/plan.md')).toBe(true);
  });

  it('keeps only the exact witnessed AskUserQuestion result as a numbered answer input', () => {
    const inputTimes = [
      '2026-10-09T04:04:40.000Z',
      '2026-10-09T04:04:50.000Z',
      '2026-10-09T04:05:00.000Z',
    ];
    const answerUUID = 'fixture-precise-full-8';
    const question = {
      tool_use_id: 'toolu-question', turn_key: 'turn-1', ts: inputTimes[0], status: 'answered', state: 'observed',
      items: [{ header: '확인 범위', question: '어느 항목을 포함할까요?', multi_select: true, options: [], answer: { labels: ['A', 'B'], free_text: null } }],
    } as AnalysisQuestion;
    const toolResult = (uuid: string, ts: string, output: string): SessionRecord => record(uuid, undefined, 1, {
      ts,
      record_type: 'user',
      view: { kind: 'tool_result', tool_results: [{ id: 'toolu-question', output }], anchor_id: uuid, dedupe_key: `source-${uuid}` },
    });
    const records = [
      record('typed-before', 'user', 1, { ts: inputTimes[0] }),
      toolResult(answerUUID, inputTimes[1], 'private normalized tool result body'),
      record('typed-after', 'user', 2, { ts: inputTimes[2] }),
      toolResult('unwitnessed-tool-result', '2026-10-09T04:05:01.000Z', 'ordinary tool output'),
    ];
    const analysis = baseAnalysis({
      header: { counts: { inputs: 3 } },
      turns: [{ key: 'turn-1', started_at: inputTimes[0], ended_at: inputTimes[2], last_assistant_message_id: 'assistant-final' }],
      inputs: [
        { uuid: 'typed-before', ts: inputTimes[0], n: 1, kind: 'typed', turn_key: 'turn-1', images: 0 },
        { uuid: answerUUID, ts: inputTimes[1], n: 2, kind: 'answer', turn_key: 'turn-1', images: 0 },
        { uuid: 'typed-after', ts: inputTimes[2], n: 3, kind: 'typed', turn_key: 'turn-1', images: 0 },
      ],
      questions: [question],
    });

    const result = buildTimeline(records, analysis);
    const answer = result.messages.find((message) => message.id === answerUUID);
    expect(answer).toMatchObject({ role: 'user', source: 'answer', source_witness: 'analysis_input_uuid', n: 2, of: 3, turn_key: 'turn-1', question });
    expect(isInput(answer!)).toBe(true);
    expect(answer?.text).toBeUndefined();
    expect(filterMessages(result.messages, { userOnly: true, hideIntermediate: false }).map((message) => message.id)).toEqual([
      'typed-before', answerUUID, 'typed-after',
    ]);
    expect(result.messages.some((message) => message.id === 'unwitnessed-tool-result')).toBe(false);
    expect(JSON.stringify(result.messages)).not.toContain('private normalized tool result body');
    expect(result.freshness).toMatchObject({ state: 'fresh', matchedInputs: 3, expectedInputs: 3 });
  });

  it('does not promote tool results with a mismatched UUID, timestamp, or input kind', () => {
    const ts = '2026-10-09T04:04:50.000Z';
    const cases = [
      { uuid: 'same-id-wrong-time', recordUUID: 'same-id-wrong-time', recordTs: '2026-10-09T04:04:51.000Z', kind: 'answer' as const },
      { uuid: 'typed-witness', recordUUID: 'typed-witness', recordTs: ts, kind: 'typed' as const },
      { uuid: 'answer-witness', recordUUID: 'other-record', recordTs: ts, kind: 'answer' as const },
    ];

    for (const scenario of cases) {
      const row = record(scenario.recordUUID, undefined, 1, {
        ts: scenario.recordTs,
        record_type: 'user',
        view: { kind: 'tool_result', tool_results: [{ id: 'toolu-question', output: 'hidden' }], anchor_id: scenario.recordUUID, dedupe_key: scenario.recordUUID },
      });
      const result = buildTimeline([row], baseAnalysis({
        header: { counts: { inputs: 1 } },
        inputs: [{ uuid: scenario.uuid, ts, n: 1, kind: scenario.kind, turn_key: 'turn-1', images: 0 }],
      }));
      expect(result.messages.some((message) => message.role === 'user' && message.source === 'answer')).toBe(false);
      expect(filterMessages(result.messages, { userOnly: true, hideIntermediate: false })).toHaveLength(0);
    }
  });

  it('uses the exact Codex root-turn metadata before any timestamp fallback', () => {
    const codexTurn = { key: 'root-turn', started_at: stamp(1), ended_at: stamp(8), last_assistant_message_id: 'codex-final' };
    const result = buildTimeline([
      record('codex-input', 'user', 2, { codex_root_turn_id: 'root-turn', codex_turn_metadata_known: true }),
    ], baseAnalysis({ turns: [codexTurn], inputs: [] }));
    expect(result.messages[0]).toMatchObject({ role: 'user', source: 'unknown', turn_key: 'root-turn' });
    expect(result.messages[0].n).toBeUndefined();
  });

  it('uses exact Codex root-turn rows to distinguish intermediate and final output', () => {
    const turn = { key: 'root-turn', started_at: stamp(1), ended_at: stamp(8), last_assistant_message_id: '' };
    const result = buildTimeline([
      record('codex-mid', 'assistant', 3, { codex_root_turn_id: 'root-turn', codex_turn_metadata_known: true }),
      record('codex-final', 'assistant', 7, { codex_root_turn_id: 'root-turn', codex_turn_metadata_known: true }),
    ], baseAnalysis({ turns: [turn] }));
    expect(result.messages.find((message) => message.id === 'codex-mid')?.intermediate).toBe(true);
    expect(result.messages.find((message) => message.id === 'codex-final')?.intermediate).toBe(false);
  });

  it('exposes freshness, truncation, and non-owner limits instead of inventing input counts', () => {
    const records = [record('typed', 'user', 1)];
    const stale = buildTimeline(records, baseAnalysis({ state: 'stale', inputs: [input('typed', 1, 'typed', 'turn-1', 1)] }));
    expect(stale.freshness.state).toBe('stale');
    const partial = buildTimeline(records, baseAnalysis({ truncated: { turns: false, abandoned: false, inputs: true, questions: false, plans: false, recaps: false, bodies: false } }));
    expect(partial.freshness.state).toBe('partial');
    const nonOwner = buildTimeline(records, baseAnalysis({ owner_view: false, inputs: [input('typed', 1, 'typed', 'turn-1', 1)] }));
    expect(nonOwner.inputCount).toBeNull();
    expect(nonOwner.freshness.state).toBe('unavailable');
    expect(nonOwner.messages[0]).toMatchObject({ role: 'user', source: 'unknown' });
    expect(nonOwner.messages[0].n).toBeUndefined();
  });
});
