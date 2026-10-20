'use client';

import type { SessionClassification, SessionClassificationCounts } from '@/lib/session-analysis-types';
import { Lock } from 'lucide-react';
import { CLASSIFICATION_HINT, CLASSIFICATION_LABEL, CLASSIFICATION_ORDER } from './list-model';
import { SegmentedTabs, type TabItem } from './segmented-tabs';

/**
 * 사용자 분류 탭(F5): 미분류 · 상담 중 · 작업 중 · 나중에 · 완료, 각각 개수. 레퍼런스의 다섯 탭 앞에 "전체"를 하나 더 둔다 —
 * 분류를 거치지 않은 전체 목록을 볼 길이 필요해서다. 분류는 세션 주인에게만 온다(개인 정리 값이다).
 */

type ClassificationFilter = SessionClassification | 'all';

interface ClassificationTabsProps {
  value: ClassificationFilter;
  counts?: SessionClassificationCounts;
  /** 전체 개수. 분류가 없는 행도 센다. 조회 전에는 표시하지 않는다. */
  total?: number;
  onChange: (value: ClassificationFilter) => void;
  /** 선택된 탭이 가리키는 목록 패널의 id. */
  panelId?: string;
  /** 비소유 관리자: 분류는 주인의 개인 값이라 API가 주지 않는다. */
  unavailable?: boolean;
}

const ClassificationTabs = ({ value, counts, total, onChange, panelId, unavailable = false }: ClassificationTabsProps) => {
  if (unavailable) {
    return (
      <p className="flex items-center gap-1.5 text-[11px] text-ink-3">
        <Lock size={12} strokeWidth={1.75} aria-hidden="true" />
        분류와 메모는 세션 주인에게만 제공됩니다
      </p>
    );
  }
  const items: TabItem<ClassificationFilter>[] = [
    { value: 'all', label: '전체', count: total, hint: '분류와 상관없이 모든 세션' },
    ...CLASSIFICATION_ORDER.map((key) => ({ value: key, label: CLASSIFICATION_LABEL[key], count: counts?.[key], hint: CLASSIFICATION_HINT[key] })),
  ];
  return <SegmentedTabs items={items} value={value} onChange={onChange} ariaLabel="사용자 분류" idPrefix="classification" panelId={panelId} />;
};

export { ClassificationTabs };
export type { ClassificationFilter };
