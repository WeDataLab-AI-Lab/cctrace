import { describe, expect, it } from 'vitest';
import type { AnalysisOutputs, SessionCommits, SessionTasks, TaskUpdate } from '@/lib/session-analysis-types';
import { commitHashText, commitsSummary, editedFileCount, filesSummary, taskActionStates, taskActionsText, tasksSummary } from './outcomes';

const task = (actions: Partial<TaskUpdate['actions']>): TaskUpdate => ({
  id: 'task-1',
  kind: 'task',
  title: 't',
  actions: { added: false, completed: false, edited: false, commit_linked: false, ...actions },
});

describe('작업·이슈 갱신', () => {
  it('동작 아이콘은 추가·완료·수정·커밋 연결 순이고, 켜진 것만 lit이다', () => {
    const states = taskActionStates(task({ added: true, commit_linked: true }));
    expect(states.map((s) => s.label)).toEqual(['추가', '완료', '수정', '커밋 연결']);
    expect(states.map((s) => s.lit)).toEqual([true, false, false, true]);
    expect(taskActionsText(task({ added: true, commit_linked: true }))).toBe('추가·커밋 연결');
    expect(taskActionsText(task({}))).toBe('동작 없음');
  });

  it('빈 목록은 "작업·이슈 갱신 없음", 목록이 아예 안 왔으면 받지 못함(없음이 아니다)', () => {
    expect(tasksSummary({ items: [] })).toMatchObject({ mark: 'absent', title: '작업·이슈 갱신 없음', expandable: false });
    expect(tasksSummary(undefined)).toMatchObject({ mark: 'unknown', expandable: false });
    expect(tasksSummary(undefined).title).toContain('받지 못함');
  });

  it('있으면 펼칠 수 있다', () => {
    const tasks: SessionTasks = { items: [task({ added: true })] };
    expect(tasksSummary(tasks)).toMatchObject({ mark: 'present', title: '작업·이슈 갱신', expandable: true });
  });
});

describe('커밋', () => {
  const commits = (over: Partial<SessionCommits>): SessionCommits => ({ state: 'observed', total: 0, items: [], truncated: false, ...over });

  it('관측: N건, 0건은 "커밋 없음"(모름이 아니다)', () => {
    expect(commitsSummary(commits({ total: 10, items: [{ hash: null, short: 'a1', subject: 's', ts: '2026-10-01T00:00:00Z', tasks: [] }] }))).toMatchObject({
      mark: 'present',
      title: '커밋 10건',
      expandable: true,
    });
    expect(commitsSummary(commits({ total: 0 }))).toMatchObject({ mark: 'absent', title: '커밋 없음', expandable: false });
  });

  it('수집하지 않음과 판단 불가는 0이 아니라 점선(unknown)이고 이유를 말한다', () => {
    const notCollected = commitsSummary({ state: 'not_collected', total: null, items: [], truncated: false });
    expect(notCollected).toMatchObject({ mark: 'unknown', title: '커밋 수집 안 함' });
    expect(notCollected.hint).toContain('없었다는 뜻이 아닙니다');
    const undeterminable = commitsSummary({ state: 'undeterminable', total: null, items: [], truncated: false });
    expect(undeterminable).toMatchObject({ mark: 'unknown', title: '커밋 판단 불가' });
    expect(undeterminable.hint).toContain('0건이 아닙니다');
    expect(commitsSummary(undefined)).toMatchObject({ mark: 'unknown' });
  });

  it('상한에서 잘렸어도 제목의 수는 전체 수다', () => {
    const s = commitsSummary(commits({ total: 130, truncated: true, items: Array.from({ length: 100 }, (_, i) => ({ hash: null, short: `h${i}`, subject: 's', ts: '2026-10-01T00:00:00Z', tasks: [] })) }));
    expect(s.title).toBe('커밋 130건');
  });

  it('해시 칩 글자: short, 없으면 전체의 앞 7자, 둘 다 없으면 null("해시 불명")', () => {
    const base = { subject: 's', ts: '2026-10-01T00:00:00Z', tasks: [] };
    expect(commitHashText({ ...base, hash: 'abcdef0123', short: '441e4b2' })).toBe('441e4b2');
    expect(commitHashText({ ...base, hash: 'abcdef0123456', short: null })).toBe('abcdef0');
    expect(commitHashText({ ...base, hash: null, short: null })).toBeNull();
  });
});

describe('변경한 파일', () => {
  const outputs = (over: Partial<AnalysisOutputs['edited_files']>, prs = 0): AnalysisOutputs => ({
    edited_files: { state: 'observed', active_total: 3, abandoned_total: 0, truncated: false, items: [], ...over },
    lines: null,
    prs: { items: Array.from({ length: prs }, (_, i) => ({ url: `https://git.example.com/p/${i}`, number: i, repository: 'r', first_seen_at: '2026-10-01T00:00:00Z', turn_key: null })), truncated: false },
  });

  it('fixture-p4-git-witness-v2: 같은 파일 두 번 편집은 파일 한 개와 편집 두 회로 센다', () => {
    const witness = outputs({
      active_total: 2,
      items: [{ path: 'fixture.txt', edits: 2, subagent_edits: 0, abandoned_edits: 0 }],
    });
    expect(editedFileCount(witness.edited_files)).toBe(1);
    expect(filesSummary(witness, 'claude')).toMatchObject({ mark: 'present', title: '변경한 파일 1건', expandable: true });
    expect(witness.edited_files.active_total).toBe(2);
  });

  it('서로 다른 관측 경로는 고유 경로 수로 센다', () => {
    const distinct = outputs({
      active_total: 6,
      items: [
        { path: 'a.txt', edits: 4, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'b.txt', edits: 2, subagent_edits: 0, abandoned_edits: 0 },
      ],
    });
    expect(editedFileCount(distinct.edited_files)).toBe(2);
    expect(filesSummary(distinct, 'claude').title).toBe('변경한 파일 2건');
  });

  it('관측 파일 목록이 잘리면 확인한 경로 수와 전체 불명을 나눠 알린다', () => {
    const summary = filesSummary(outputs({
      active_total: 128,
      truncated: true,
      items: [{ path: 'fixture.txt', edits: 128, subagent_edits: 0, abandoned_edits: 0 }],
    }), 'claude');
    expect(summary).toMatchObject({ mark: 'present', title: '확인한 파일 1건', expandable: true });
    expect(summary.hint).toContain('전체 파일 수를 알 수 없습니다');
  });

  it('active_total이 편집 호출 수로 알려지면 경로 가림 안내에만 회수를 적는다', () => {
    const summary = filesSummary(outputs({ state: 'redacted', active_total: 7 }), 'claude');
    expect(summary.title).toBe('변경한 파일 경로 가려짐');
    expect(summary.hint).toContain('편집 호출 7회');
  });

  it('편집이 없고 PR도 없으면 "변경한 파일 없음"', () => {
    expect(filesSummary(outputs({ active_total: 0 }), 'claude')).toMatchObject({ mark: 'absent', title: '변경한 파일 없음', expandable: false });
    expect(filesSummary(outputs({ active_total: 0 }, 1), 'claude').mark).toBe('present');
  });

  it('경로가 가려졌으면 파일 수가 아니라 가려짐이라고 적는다(호출 수를 파일 수로 쓰지 않는다)', () => {
    const s = filesSummary(outputs({ state: 'redacted', active_total: 7 }), 'claude');
    expect(s).toMatchObject({ mark: 'unknown', title: '변경한 파일 경로 가려짐' });
    expect(editedFileCount({ state: 'redacted', active_total: 7, abandoned_total: 0, truncated: false, items: [] })).toBeNull();
  });

  it('Codex는 편집을 수집하지 않아 "수집 안 함"이지 0이 아니다', () => {
    const s = filesSummary(outputs({ active_total: null, items: [] }), 'codex');
    expect(s).toMatchObject({ mark: 'unknown', title: '변경한 파일 수집 안 함' });
  });

  it('active_total이 없으면 항목 수로 대신하고, 둘 다 없으면 알 수 없음', () => {
    const items = [{ path: 'a', edits: 1, subagent_edits: 0, abandoned_edits: 0 }];
    expect(editedFileCount({ state: 'observed', active_total: null, abandoned_total: null, truncated: false, items })).toBe(1);
    expect(editedFileCount({ state: 'observed', active_total: null, abandoned_total: null, truncated: false, items: [] })).toBeNull();
    expect(filesSummary(outputs({ active_total: null, items: [] }), 'claude').title).toBe('변경한 파일 알 수 없음');
  });
});
