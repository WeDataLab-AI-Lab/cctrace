'use client';

import { createContext, useContext, useState, useEffect, type ReactNode } from 'react';
import { AGENT_KEY as STORAGE_KEY, FILTER_RESET_EVENT } from '@/lib/filter-reset';

// The harness that ran the session, not who was billed for it. Claude Code and Codex are
// harnesses you can plug another model into, so billing is a separate axis (ModelCategory
// in lib/colors.ts) and the two can combine freely.
//
// 'other' is every harness that is not one of the two named ones -- today gjc and omo. It
// is one option rather than one per tool so the pill row stays short as tools are added,
// and so a harness added later is reachable the day it starts syncing.
//
// 'weekly' is not a harness anyone ran: it is the server's weekly AI report runs, kept
// out of 'other' so it never reads as a person's usage (lib/weekly-usage.ts).
type AgentScope = '' | 'claude' | 'codex' | 'other' | 'weekly'; // '' = all

interface AgentContextType {
  selectedAgent: AgentScope;
  setSelectedAgent: (agent: AgentScope) => void;
  isHydrated: boolean;
}

const AgentContext = createContext<AgentContextType>({
  selectedAgent: '',
  setSelectedAgent: () => {},
  isHydrated: false,
});

const VALID: AgentScope[] = ['', 'claude', 'codex', 'other', 'weekly'];
const isAgentScope = (value: string): value is AgentScope =>
  VALID.some((scope) => scope === value);

const AgentProvider = ({ children }: { children: ReactNode }) => {
  const [selectedAgent, setSelectedAgentState] = useState<AgentScope>('');
  const [isHydrated, setIsHydrated] = useState(false);

  const setSelectedAgent = (agent: AgentScope) => {
    setSelectedAgentState(agent);
    try { localStorage.setItem(STORAGE_KEY, agent); } catch { /* ignore */ }
  };

  /** 마운트 후 localStorage에서 1회 복원. 정적 export 하이드레이션 불일치를 피하려 의도적으로 effect에서 setState. */
  useEffect(() => {
    try {
      const v = localStorage.getItem(STORAGE_KEY);
      if (v !== null && isAgentScope(v)) {
        // eslint-disable-next-line react-hooks/set-state-in-effect -- 의도된 post-mount 하이드레이션
        setSelectedAgentState(v);
      }
    } catch { /* ignore */ }
    setIsHydrated(true);
  }, []);

  /** 로고 클릭(명시적 초기화) 시 Agent 범위를 전체로 되돌린다. 저장소는 이미 비워졌고, 여기서는 마운트된 상태의 값만 따라간다. */
  useEffect(() => {
    const onReset = () => setSelectedAgentState('');
    window.addEventListener(FILTER_RESET_EVENT, onReset);
    return () => window.removeEventListener(FILTER_RESET_EVENT, onReset);
  }, []);

  return (
    <AgentContext.Provider value={{ selectedAgent, setSelectedAgent, isHydrated }}>
      {children}
    </AgentContext.Provider>
  );
};

const useAgent = () => useContext(AgentContext);

export { AgentProvider, useAgent };
export type { AgentScope };
