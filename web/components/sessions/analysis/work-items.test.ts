import { describe, expect, it } from 'vitest';
import type { WorkItem } from '@/lib/session-analysis-types';
import { WORK_CHILD_LABEL, WORK_KIND_LABEL, checkboxState, workChildLabel, workKindLabel, workRows, workSummary } from './work-items';

const item = (over: Partial<WorkItem> & Pick<WorkItem, 'id'>): WorkItem => ({
  kind: 'followup',
  done: true,
  text: '요청',
  commits: [],
  tasks: [],
  ...over,
});

describe('작업 항목 줄', () => {
  it('종류 이름은 상담·의뢰·추가 의뢰·마무리, 하위 항목은 작업·확인', () => {
    expect(Object.values(WORK_KIND_LABEL)).toEqual(['상담', '의뢰', '추가 의뢰', '마무리']);
    expect(Object.values(WORK_CHILD_LABEL)).toEqual(['작업', '확인']);
    expect(workKindLabel('followup')).toBe('추가 의뢰');
    expect(workKindLabel('mystery')).toBe('mystery');
    expect(workChildLabel('check')).toBe('확인');
  });

  it('체크 글리프는 완료·미완·확인 못 함이고, null은 미완이 아니다', () => {
    expect(checkboxState(true)).toBe('done');
    expect(checkboxState(false)).toBe('open');
    expect(checkboxState(null)).toBe('unknown');
  });

  it('결정과 메모는 앞뒤 공백을 걷고 비면 null이다', () => {
    const [row] = workRows([item({ id: 'a', kind: 'consultation', decision: '  서버에 저장  ', note: '   ' })]);
    expect(row.decision).toBe('서버에 저장');
    expect(row.note).toBeNull();
    expect(workRows([item({ id: 'b' })])[0].decision).toBeNull();
  });

  it('같은 커밋은 한 번만 칩이 되고 순서를 지킨다', () => {
    const [row] = workRows([item({ id: 'a', commits: ['441e4b2', 'f866fbe', '441e4b2'], tasks: ['task-1', 'task-1'] })]);
    expect(row.commits).toEqual(['441e4b2', 'f866fbe']);
    expect(row.tasks).toEqual(['task-1']);
  });

  it('표시 task id와 저장소가 붙은 P4 item_key를 함께 보존한다', () => {
    const [row] = workRows([item({ id: 'a', tasks: ['#9'], task_keys: ['repo-a:#9', 'repo-b:#9'] })]);
    expect(row.tasks).toEqual(['#9']);
    expect(row.taskKeys).toEqual(['repo-a:#9', 'repo-b:#9']);
  });

  it('턴을 모르는 줄은 turnKey가 null이라 눌리지 않는다', () => {
    const rows = workRows([item({ id: 'a', turn_key: 't01' }), item({ id: 'b' })]);
    expect(rows.map((r) => r.turnKey)).toEqual(['t01', null]);
  });

  it('하위 항목은 종류 이름과 체크 상태를 갖는다', () => {
    const [row] = workRows([
      item({
        id: 'a',
        children: [
          { kind: 'work', done: true, text: '구현' },
          { kind: 'check', done: null, text: '확인' },
        ],
      }),
    ]);
    expect(row.children).toEqual([
      { kind: 'work', kindLabel: '작업', state: 'unknown', suggestedState: 'done', text: '구현' },
      { kind: 'check', kindLabel: '확인', state: 'unknown', suggestedState: 'unknown', text: '확인' },
    ]);
  });

  it('done 값은 모델 제안으로 보존하지만 검증 전 상태는 모두 판단 불가다', () => {
    const items = [item({ id: 'a', done: true }), item({ id: 'b', done: false }), item({ id: 'c', done: null })];
    const rows = workRows(items);
    expect(rows.map(({ state, suggestedState }) => [state, suggestedState])).toEqual([
      ['unknown', 'done'], ['unknown', 'open'], ['unknown', 'unknown'],
    ]);
    expect(items.map(({ done }) => done)).toEqual([true, false, null]);
  });

  it('입력 순서를 그대로 지킨다', () => {
    expect(workRows([item({ id: 'z' }), item({ id: 'a' }), item({ id: 'm' })]).map((r) => r.id)).toEqual(['z', 'a', 'm']);
  });
});

describe('작업 상황 요약', () => {
  it('최상위 항목만 센다. 하위 항목의 미완은 세지 않는다', () => {
    const summary = workSummary([
      item({ id: 'a' }),
      item({ id: 'b', done: false }),
      item({ id: 'c', done: null }),
      item({ id: 'd', children: [{ kind: 'work', done: false, text: 'x' }] }),
    ]);
    expect(summary).toEqual({ total: 4 });
    expect(workSummary([
      item({ id: 'a', done: false }), item({ id: 'b', done: false }), item({ id: 'c', done: false }), item({ id: 'd', done: false }),
    ])).toEqual(summary);
  });

  it('빈 목록은 모두 0이다', () => {
    expect(workSummary([])).toEqual({ total: 0 });
  });
});
