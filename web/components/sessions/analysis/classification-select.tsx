'use client';

import type { SessionClassification } from '@/lib/session-analysis-types';
import { CLASSIFICATION_HINT, CLASSIFICATION_LABEL, CLASSIFICATION_ORDER } from './list-model';

/**
 * 한 세션의 분류를 바꾸는 선택 상자(F5). 제어형이다. 분류 탭이 목록을 거르고, 이 상자가 값을 정한다.
 * 분류는 세션 주인의 개인 값이라 비소유 범위에서는 쓰지 않는다.
 */

interface ClassificationSelectProps {
  value: SessionClassification;
  onChange: (next: SessionClassification) => void;
  /** 접근성 이름에 들어갈 세션 표시. */
  sessionLabel: string;
}

const ClassificationSelect = ({ value, onChange, sessionLabel }: ClassificationSelectProps) => (
  <label className="inline-flex items-center gap-1.5 text-[11px] text-ink-3">
    분류
    <select
      aria-label={`${sessionLabel}의 분류`}
      title={CLASSIFICATION_HINT[value]}
      value={value}
      onChange={(e) => {
        const next = CLASSIFICATION_ORDER.find((c) => c === e.target.value);
        if (next) onChange(next);
      }}
      className="h-6 rounded border border-border bg-surface px-1 text-[11.5px] text-ink"
    >
      {CLASSIFICATION_ORDER.map((c) => (
        <option key={c} value={c}>
          {CLASSIFICATION_LABEL[c]}
        </option>
      ))}
    </select>
  </label>
);

export { ClassificationSelect };
