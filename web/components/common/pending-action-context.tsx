'use client';

import { createContext, useContext, useState, useRef, useEffect, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { deleteUserData, mergeUsers } from '@/lib/api';
import type { CostSummary } from '@/lib/types';

type PendingAction =
  | { type: 'delete'; user: CostSummary }
  | { type: 'merge'; from: CostSummary; to: CostSummary };

interface PendingActionContextValue {
  pendingAction: PendingAction | null;
  setPendingAction: (action: PendingAction | null) => void;
}

interface UndoToastProps {
  message: string;
  onCancel: () => void;
  onExpire: () => void;
}

const PendingActionContext = createContext<PendingActionContextValue>({
  pendingAction: null,
  setPendingAction: () => {},
});

const usePendingAction = () => useContext(PendingActionContext);

const resolveLabel = (user: CostSummary): string => user.profile_email || 'Unknown';

const toastMessage = (action: PendingAction): string => {
  if (action.type === 'delete') return `${resolveLabel(action.user)} 삭제 중...`;
  return `${resolveLabel(action.from)} → ${resolveLabel(action.to)} 병합 중...`;
};

const UndoToast = ({ message, onCancel, onExpire }: UndoToastProps) => {
  const [countdown, setCountdown] = useState(5);
  const total = 5;
  const cancelledRef = useRef(false);

  const handleCancel = () => {
    cancelledRef.current = true;
    onCancel();
  };

  const circumference = 2 * Math.PI * 10;
  const offset = circumference * (1 - countdown / total);

  /** 1초마다 카운트다운. 0이 되면(취소되지 않았다면) onExpire를 실행한다. */
  useEffect(() => {
    if (countdown <= 0) {
      if (!cancelledRef.current) onExpire();
      return;
    }
    const t = setTimeout(() => setCountdown((c) => c - 1), 1000);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [countdown]);

  return (
    <div className="fixed bottom-6 right-6 z-50 flex items-center gap-3 bg-brand text-brand-ink rounded-xl px-4 py-3 shadow-2xl animate-in slide-in-from-bottom-2">
      <div className="relative w-7 h-7 flex-shrink-0">
        <svg className="w-7 h-7 -rotate-90" viewBox="0 0 24 24">
          <circle cx="12" cy="12" r="10" fill="none" stroke="rgba(255,255,255,0.2)" strokeWidth="2" />
          <circle
            cx="12"
            cy="12"
            r="10"
            fill="none"
            stroke="white"
            strokeWidth="2"
            strokeDasharray={circumference}
            strokeDashoffset={offset}
            className="transition-[stroke-dashoffset] duration-1000 ease-linear"
          />
        </svg>
        <span className="absolute inset-0 flex items-center justify-center text-[10px] font-bold">{countdown}</span>
      </div>
      <span className="text-sm">{message}</span>
      <button
        onClick={handleCancel}
        className="ml-1 text-sm font-semibold text-white/80 hover:text-white underline underline-offset-2 transition-colors"
      >
        취소
      </button>
    </div>
  );
};

const PendingActionProvider = ({ children }: { children: ReactNode }) => {
  const [pendingAction, setPendingAction] = useState<PendingAction | null>(null);
  const queryClient = useQueryClient();

  const executeAction = async (action: PendingAction) => {
    if (action.type === 'delete') {
      await deleteUserData(action.user.profile_email, action.user.user_id || '');
    } else {
      await mergeUsers(action.from.profile_email, action.from.user_id || '', action.to.profile_email);
    }
    queryClient.invalidateQueries({ queryKey: ['cost-by-user'] });
    setPendingAction(null);
  };

  const handleUndoCancel = () => setPendingAction(null);
  const handleUndoExpire = () => {
    if (pendingAction) executeAction(pendingAction);
  };

  return (
    <PendingActionContext.Provider value={{ pendingAction, setPendingAction }}>
      {children}
      {pendingAction && (
        <UndoToast
          key={JSON.stringify(pendingAction)}
          message={toastMessage(pendingAction)}
          onCancel={handleUndoCancel}
          onExpire={handleUndoExpire}
        />
      )}
    </PendingActionContext.Provider>
  );
};

export { PendingActionProvider, usePendingAction };
export type { PendingAction };
