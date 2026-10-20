/**
 * 목록 카드(F1)의 묶음과 분류의 순수 로직.
 *
 *   사용자 분류(F5)   미분류·상담 중·작업 중·나중에·완료 탭과 개수
 *   실행 중인 세션(F2) 살아 있는 세션과 처리 중·완료·중단 중 개수
 *   함께 끝난 세션(F3) 같은 시각에 끝난 세션 묶음. "떼기"로 묶음에서 빼면 일반 목록으로 간다
 *
 * 모두 행(`SessionOverview.analysis`)만 읽는다. 범주(`category`)와 판정(`judgement`)은 세션 주인 범위에서만 오므로,
 * 비소유 범위의 행에는 묶음도 분류도 없다 — 이 파일은 그때 빈 결과를 돌려주고 화면이 그리지 않는다.
 */

import type {
  JudgementBucket,
  SessionCategoryKey,
  SessionClassification,
  SessionClassificationCounts,
} from '@/lib/session-analysis-types';
import type { SessionOverview } from '@/lib/types';
import { formatDateTime } from './format';

/* ---------------- 사용자 분류 (F5) ---------------- */

const CLASSIFICATION_ORDER: readonly SessionClassification[] = ['unsorted', 'consulting', 'working', 'later', 'done'];

const CLASSIFICATION_LABEL: Record<SessionClassification, string> = {
  unsorted: '미분류',
  consulting: '상담 중',
  working: '작업 중',
  later: '나중에',
  done: '완료',
};

const CLASSIFICATION_HINT: Record<SessionClassification, string> = {
  unsorted: '아직 분류하지 않은 세션',
  consulting: '상담하며 방향을 정하는 중인 세션',
  working: '작업을 이어 가는 중인 세션',
  later: '나중에 다시 볼 세션',
  done: '정리를 마친 세션',
};

/** 분류가 오지 않는 행(비소유 범위, 서버가 아직 안 보냄)은 null이다. 미분류와 다르다. */
const classificationOf = (row: SessionOverview): SessionClassification | null => row.analysis?.classification ?? null;

/** 분류 탭의 개수. 분류가 없는 행은 어느 탭에도 세지 않는다. */
const classificationCounts = (rows: readonly SessionOverview[]): SessionClassificationCounts => {
  const counts: SessionClassificationCounts = { unsorted: 0, consulting: 0, working: 0, later: 0, done: 0 };
  for (const row of rows) {
    const c = classificationOf(row);
    if (c) counts[c] += 1;
  }
  return counts;
};

/* ---------------- 상태별 개수 필터 (B3) ---------------- */

const matchesBucket = (row: SessionOverview, bucket: JudgementBucket | null): boolean =>
  bucket === null || row.analysis?.judgement?.bucket === bucket;

/* ---------------- 실행 중인 세션 (F2) ---------------- */

/** 살아 있는 실행 인스턴스가 있는 범주. `stale_live`(오래된 생존 행)는 믿지 않으므로 뺀다. */
const ALIVE_CATEGORIES: ReadonlySet<SessionCategoryKey> = new Set<SessionCategoryKey>(['waiting', 'running', 'idle']);

const isAlive = (row: SessionOverview): boolean => {
  const category = row.analysis?.category;
  return category !== undefined && ALIVE_CATEGORIES.has(category);
};

interface RunningCounts {
  total: number;
  /** 지금 일하는 중(관측 범주 running). */
  processing: number;
  /** 사용자의 응답을 기다리는 중(관측 범주 waiting). 권한 승인, 질문 같은 것이다. */
  waiting: number;
  /** 살아 있고, 턴이 끝났고, 남은 것이 관측되지 않았다. */
  done: number;
  /** 사용자가 중단한 채로 살아 있다. */
  interrupted: number;
  /** 살아 있고, 턴은 끝났지만 남은 것이 있다. */
  leftover: number;
  /** 판정을 못 했거나 판정이 오지 않았다. */
  unknown: number;
}

/**
 * 살아 있는 세션을 한 곳에만 센다: 중단 중 → 응답 대기 → 처리 중 → (그 밖은 버킷) 완료·작업 도중·모름.
 * 합이 `total`과 같다.
 */
const runningCounts = (rows: readonly SessionOverview[]): RunningCounts => {
  const counts: RunningCounts = { total: 0, processing: 0, waiting: 0, done: 0, interrupted: 0, leftover: 0, unknown: 0 };
  for (const row of rows) {
    if (!isAlive(row)) continue;
    counts.total += 1;
    const judgement = row.analysis?.judgement;
    if (judgement?.status === 'interrupted') counts.interrupted += 1;
    else if (row.analysis?.category === 'waiting') counts.waiting += 1;
    else if (row.analysis?.category === 'running') counts.processing += 1;
    else if (judgement?.bucket === 'done') counts.done += 1;
    else if (judgement?.bucket === 'leftover') counts.leftover += 1;
    else counts.unknown += 1;
  }
  return counts;
};

/* ---------------- 함께 끝난 세션 (F3) ---------------- */

/**
 * 묶음에서 가장 늦게 끝난 세션보다 이 간격(밀리초) 안에 끝난 세션은 함께 끝난 것으로 본다. 앱을 닫을 때 세션이 줄줄이 끝나는 모양을 잡는다.
 * 앞 세션이 아니라 가장 늦은 세션을 기준으로 재므로, 하나씩 이어 끝나는 긴 사슬이 하나의 묶음으로 불어나지 않는다.
 */
const ENDED_TOGETHER_GAP_MS = 60_000;
/** 묶음은 둘 이상일 때만 만든다. */
const ENDED_TOGETHER_MIN = 2;

interface EndedGroup {
  key: string;
  /** "10/07 15:12에 함께 끝난 2건". */
  label: string;
  /** 묶음에서 가장 늦게 끝난 시각. */
  endedAt: string;
  rows: SessionOverview[];
}

const endMs = (row: SessionOverview): number => {
  const ms = Date.parse(row.end_time);
  return Number.isNaN(ms) ? 0 : ms;
};

interface RowGroups {
  running: SessionOverview[];
  ended: EndedGroup[];
  rest: SessionOverview[];
}

/**
 * 행을 세 곳으로 나눈다. 입력 순서는 각 곳 안에서 지킨다(묶음 안은 늦게 끝난 순).
 * `dismissed`는 "떼기"한 세션이다 — 함께 끝난 묶음에서만 빠지고 일반 목록으로 간다.
 */
const groupRows = (rows: readonly SessionOverview[], dismissed: ReadonlySet<string> = new Set()): RowGroups => {
  const running: SessionOverview[] = [];
  const endedCandidates: SessionOverview[] = [];
  const rest: SessionOverview[] = [];
  for (const row of rows) {
    if (isAlive(row)) running.push(row);
    // 끝난 시각을 읽을 수 없는 행은 "같은 시각에 끝났다"를 말할 수 없으므로 묶지 않는다.
    else if (row.analysis?.category === 'ended' && !dismissed.has(row.session_id) && !Number.isNaN(Date.parse(row.end_time))) endedCandidates.push(row);
    else rest.push(row);
  }

  const sorted = [...endedCandidates].sort((a, b) => endMs(b) - endMs(a));
  const clusters: SessionOverview[][] = [];
  for (const row of sorted) {
    const current = clusters[clusters.length - 1];
    const latest = current ? current[0] : undefined;
    if (current && latest && endMs(latest) - endMs(row) <= ENDED_TOGETHER_GAP_MS) current.push(row);
    else clusters.push([row]);
  }

  const ended: EndedGroup[] = [];
  const leftovers = new Set<string>();
  for (const cluster of clusters) {
    if (cluster.length >= ENDED_TOGETHER_MIN) {
      const latest = cluster[0];
      ended.push({
        key: `ended-${endMs(latest)}`,
        label: `${formatDateTime(latest.end_time)}에 함께 끝난 ${cluster.length}건`,
        endedAt: latest.end_time,
        rows: cluster,
      });
    } else {
      for (const row of cluster) leftovers.add(row.session_id);
    }
  }
  // 묶음이 되지 못한 세션은 원래 순서대로 일반 목록에 돌려놓는다.
  const restSet = new Set(rest.map((r) => r.session_id));
  const merged = rows.filter((r) => restSet.has(r.session_id) || leftovers.has(r.session_id));
  return { running, ended, rest: merged };
};

export {
  CLASSIFICATION_ORDER,
  CLASSIFICATION_LABEL,
  CLASSIFICATION_HINT,
  classificationOf,
  classificationCounts,
  matchesBucket,
  isAlive,
  runningCounts,
  groupRows,
  ENDED_TOGETHER_GAP_MS,
};
export type { RunningCounts, EndedGroup, RowGroups };
