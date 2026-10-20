/**
 * 작업 상황 체크리스트(D15)를 화면 줄로 바꾼다.
 *
 * 줄 하나는 사용자가 한 요청 하나다. 검증 전에는 체크 상태를 판단 불가로 두고 원래 done 값은 모델 제안으로 보존한다.
 */

import type { WorkChild, WorkChildKind, WorkItem, WorkKind } from '@/lib/session-analysis-types';

const WORK_KIND_LABEL: Record<WorkKind, string> = {
  consultation: '상담',
  request: '의뢰',
  followup: '추가 의뢰',
  finishing: '마무리',
};

const WORK_CHILD_LABEL: Record<WorkChildKind, string> = { work: '작업', check: '확인' };

const workKindLabel = (kind: string): string => (WORK_KIND_LABEL as Record<string, string | undefined>)[kind] ?? kind;
const workChildLabel = (kind: string): string => (WORK_CHILD_LABEL as Record<string, string | undefined>)[kind] ?? kind;

type CheckboxState = 'done' | 'open' | 'unknown';

// P1.5 has no operational quality-gate pass evidence; keep model done values as suggestions.
const WORK_DONE_QUALITY_GATE_PASSED = false;

const checkboxState = (done: boolean | null): CheckboxState => (done === true ? 'done' : done === false ? 'open' : 'unknown');

const CHECKBOX_LABEL: Record<CheckboxState, string> = { done: '완료', open: '미완', unknown: '완료 여부 확인 못 함' };

interface WorkChildView {
  kind: WorkChildKind;
  kindLabel: string;
  state: CheckboxState;
  suggestedState: CheckboxState;
  text: string;
}

interface WorkRowView {
  id: string;
  kind: WorkKind;
  kindLabel: string;
  state: CheckboxState;
  suggestedState: CheckboxState;
  text: string;
  /** "→ 결정: …" 뒤의 글. 없으면 null. */
  decision: string | null;
  note: string | null;
  /** 괄호 안에 칩으로 그릴 짧은 해시. 중복은 한 번만. */
  commits: string[];
  tasks: string[];
  taskKeys: string[];
  /** 이 줄을 누르면 워터폴에서 선택할 턴. 없으면 줄은 눌리지 않는다. */
  turnKey: string | null;
  children: WorkChildView[];
}

const unique = (items: readonly string[]): string[] => [...new Set(items)];

const childView = (child: WorkChild): WorkChildView => ({
  kind: child.kind,
  kindLabel: workChildLabel(child.kind),
  state: WORK_DONE_QUALITY_GATE_PASSED ? checkboxState(child.done) : 'unknown',
  suggestedState: checkboxState(child.done),
  text: child.text,
});

const workRows = (work: readonly WorkItem[]): WorkRowView[] =>
  work.map((item) => ({
    id: item.id,
    kind: item.kind,
    kindLabel: workKindLabel(item.kind),
    state: WORK_DONE_QUALITY_GATE_PASSED ? checkboxState(item.done) : 'unknown',
    suggestedState: checkboxState(item.done),
    text: item.text,
    decision: item.decision?.trim() ? item.decision.trim() : null,
    note: item.note?.trim() ? item.note.trim() : null,
    commits: unique(item.commits),
    tasks: unique(item.tasks),
    taskKeys: unique(item.task_keys ?? []),
    turnKey: item.turn_key ?? null,
    children: (item.children ?? []).map(childView),
  }));

interface WorkSummary {
  total: number;
}

/** 검증 전에는 모델 제안 상태별 개수를 확정 집계하지 않는다. */
const workSummary = (work: readonly WorkItem[]): WorkSummary => ({ total: work.length });

export { WORK_KIND_LABEL, WORK_CHILD_LABEL, CHECKBOX_LABEL, WORK_DONE_QUALITY_GATE_PASSED, workKindLabel, workChildLabel, checkboxState, workRows, workSummary };
export type { CheckboxState, WorkRowView, WorkChildView, WorkSummary };
