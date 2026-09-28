'use client';

import { useState, useEffect, useRef } from 'react';
import { FILTER_RESET_EVENT, isFilterKey } from './filter-reset';

const usePersistedState = <T,>(
  key: string,
  defaultValue: T,
): [T, (value: T | ((prev: T) => T)) => void] => {
  const [state, setState] = useState<T>(defaultValue);
  const hydrated = useRef(false);

  /** 마운트 후 localStorage에서 1회 복원. 정적 export 하이드레이션 불일치를 피하려 의도적으로 effect에서 setState. */
  useEffect(() => {
    try {
      const stored = localStorage.getItem(key);
      if (stored) {
        const parsed = JSON.parse(stored) as T;
        // eslint-disable-next-line react-hooks/set-state-in-effect -- 의도된 post-mount 하이드레이션
        setState(parsed);
      }
    } catch { /* ignore */ }
    hydrated.current = true;
  }, [key]);

  /** 값 변경 시 localStorage에 영속화(초기 마운트는 건너뜀). */
  useEffect(() => {
    if (!hydrated.current) return;
    try {
      localStorage.setItem(key, JSON.stringify(state));
    } catch { /* ignore */ }
  }, [key, state]);

  /**
   * 로고 클릭(명시적 초기화) 시 조회 필터만 기본값으로 되돌린다. 저장소는 이미 비워졌지만
   * 값이 React state 에 남아 있어, 마운트된 채로는 이벤트 없이는 반영되지 않는다.
   * 필터가 아닌 키(예: users:activeTab)는 사용자의 현재 위치이므로 건드리지 않는다.
   */
  useEffect(() => {
    if (!isFilterKey(key)) return;
    const onReset = () => setState(defaultValue);
    window.addEventListener(FILTER_RESET_EVENT, onReset);
    return () => window.removeEventListener(FILTER_RESET_EVENT, onReset);
  }, [key, defaultValue]);

  return [state, setState];
};

export { usePersistedState };
