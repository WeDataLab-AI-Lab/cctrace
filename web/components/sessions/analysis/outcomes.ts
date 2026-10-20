/**
 * 세션 성과(D9, D10, D11)의 세 줄: 작업·이슈 갱신, 커밋, 변경한 파일.
 *
 * 줄마다 앞의 기호가 말하는 것은 하나다: 관측해서 있다(확인 표시), 관측해서 없다(빈 기호), 모른다(점선).
 * "없다"와 "모른다"를 같은 모양으로 그리지 않는다. 커밋은 수집하지 않는 클라이언트가 있어서 모름이 흔하다.
 */

import type {
  AnalysisOutputs,
  CommitItem,
  SessionCommits,
  SessionTasks,
  TaskActions,
  TaskKind,
  TaskUpdate,
} from '@/lib/session-analysis-types';
import { formatCount } from './format';

type OutcomeMark = 'present' | 'absent' | 'unknown';

interface OutcomeSummary {
  mark: OutcomeMark;
  /** 줄 제목: "작업·이슈 갱신", "커밋 10건", "변경한 파일 16건". */
  title: string;
  /** 펼쳐서 볼 것이 있는가. */
  expandable: boolean;
  /** 제목 옆의 설명(이유). 있으면 툴팁이 된다. */
  hint: string | null;
}

/* ---------------- task·이슈 갱신 ---------------- */

const TASK_ACTION_ORDER: readonly { key: keyof TaskActions; label: string }[] = [
  { key: 'added', label: '추가' },
  { key: 'completed', label: '완료' },
  { key: 'edited', label: '수정' },
  { key: 'commit_linked', label: '커밋 연결' },
];

const TASK_KIND_LABEL: Record<TaskKind, string> = { task: 'task', issue: '이슈', pr: 'PR' };

/** 동작 아이콘 넷의 켜짐. 켜진 것은 색이 있고 꺼진 것은 흐리다. */
const taskActionStates = (task: TaskUpdate): { key: keyof TaskActions; label: string; lit: boolean }[] =>
  TASK_ACTION_ORDER.map((a) => ({ ...a, lit: task.actions[a.key] === true }));

/** "추가·완료·커밋 연결" 같은 접근성용 요약. 하나도 켜지지 않았으면 "동작 없음". */
const taskActionsText = (task: TaskUpdate): string => {
  const lit = taskActionStates(task)
    .filter((a) => a.lit)
    .map((a) => a.label);
  return lit.length > 0 ? lit.join('·') : '동작 없음';
};

const tasksSummary = (tasks: SessionTasks | undefined): OutcomeSummary => {
  if (!tasks) {
    return {
      mark: 'unknown',
      title: '작업·이슈 갱신을 받지 못함',
      expandable: false,
      hint: '이 응답에는 task·이슈 정보가 담겨 있지 않습니다. 갱신이 없다는 뜻이 아닙니다.',
    };
  }
  if (tasks.items.length === 0) return { mark: 'absent', title: '작업·이슈 갱신 없음', expandable: false, hint: null };
  return { mark: 'present', title: '작업·이슈 갱신', expandable: true, hint: null };
};

/* ---------------- 커밋 ---------------- */

const commitsSummary = (commits: SessionCommits | undefined): OutcomeSummary => {
  if (!commits) {
    return {
      mark: 'unknown',
      title: '커밋을 받지 못함',
      expandable: false,
      hint: '이 응답에는 커밋 정보가 담겨 있지 않습니다. 커밋이 없다는 뜻이 아닙니다.',
    };
  }
  switch (commits.state) {
    case 'not_collected':
      return {
        mark: 'unknown',
        title: '커밋 수집 안 함',
        expandable: false,
        hint: '이 클라이언트는 git 기록을 모으지 않습니다. 커밋이 없었다는 뜻이 아닙니다.',
      };
    case 'undeterminable':
      return {
        mark: 'unknown',
        title: '커밋 판단 불가',
        expandable: false,
        hint: '도구 입력이 가려졌거나 git 기록을 받지 못해 커밋을 판단할 수 없습니다. 0건이 아닙니다.',
      };
    case 'observed': {
      const total = commits.total ?? commits.items.length;
      if (total === 0 && commits.items.length === 0) return { mark: 'absent', title: '커밋 없음', expandable: false, hint: null };
      return { mark: 'present', title: `커밋 ${formatCount(total)}건`, expandable: commits.items.length > 0, hint: null };
    }
  }
};

/** 커밋 줄의 해시 칩 글자. 모르면 null이라 "해시 불명" 칩이 된다. */
const commitHashText = (item: CommitItem): string | null => item.short ?? (item.hash ? item.hash.slice(0, 7) : null);

/* ---------------- 변경한 파일 ---------------- */

/** `active_total`은 편집 이벤트 수다. 파일 수는 관측한 고유 경로에서만 센다. */
const editedFileCount = (files: AnalysisOutputs['edited_files']): number | null => {
  if (files.state !== 'observed') return null;
  const count = new Set(files.items.map((item) => item.path)).size;
  if (files.truncated) return count > 0 ? count : null;
  if (count > 0 || files.active_total === 0) return count;
  return null;
};

const filesSummary = (outputs: AnalysisOutputs, agent: string | undefined): OutcomeSummary => {
  const files = outputs.edited_files;
  const prCount = outputs.prs.items.length;
  if (agent === 'codex' && files.items.length === 0 && prCount === 0) {
    return {
      mark: 'unknown',
      title: '변경한 파일 수집 안 함',
      expandable: true,
      hint: 'Codex 세션은 편집한 파일을 수집하지 않습니다. 편집이 없었다는 뜻이 아닙니다.',
    };
  }
  if (files.state === 'redacted') {
    return {
      mark: 'unknown',
      title: '변경한 파일 경로 가려짐',
      expandable: true,
      hint: files.active_total == null
        ? '비식별화로 편집한 경로와 편집 호출 수를 알 수 없습니다. 편집이 없었다는 뜻이 아닙니다.'
        : `비식별화로 파일 경로를 알 수 없습니다. 활성 편집 호출 ${formatCount(files.active_total)}회가 관측됐습니다.`,
    };
  }
  const count = editedFileCount(files);
  if (files.truncated) {
    const hint = '편집 파일 경로 목록이 일부만 담겨 전체 파일 수를 알 수 없습니다.';
    if (count === null) return { mark: 'unknown', title: '변경한 파일 수 알 수 없음', expandable: true, hint };
    return { mark: 'present', title: `확인한 파일 ${formatCount(count)}건`, expandable: true, hint };
  }
  if (count === null) return { mark: 'unknown', title: '변경한 파일 알 수 없음', expandable: true, hint: null };
  if (count === 0 && prCount === 0) return { mark: 'absent', title: '변경한 파일 없음', expandable: false, hint: null };
  return { mark: 'present', title: `변경한 파일 ${formatCount(count)}건`, expandable: true, hint: null };
};

export {
  TASK_ACTION_ORDER,
  TASK_KIND_LABEL,
  taskActionStates,
  taskActionsText,
  tasksSummary,
  commitsSummary,
  commitHashText,
  editedFileCount,
  filesSummary,
};
export type { OutcomeMark, OutcomeSummary };
