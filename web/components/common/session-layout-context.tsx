'use client';

import { createContext, useContext, useState } from 'react';
import type { ReactNode } from 'react';
import { usePersistedState } from '@/lib/use-persisted-state';

interface SessionLayoutState {
  menuCollapsed: boolean;
  listCollapsed: boolean;
  expanded: boolean;
  toggleMenu: () => void;
  toggleList: () => void;
  /** Fold the app menu around a session detail without overwriting the saved preference. */
  toggleDetailMenu: (folded: boolean) => void;
  setExpanded: (expanded: boolean) => void;
}
const noAction = () => undefined;
const SessionLayoutContext = createContext<SessionLayoutState>({
  menuCollapsed: false, listCollapsed: false, expanded: false,
  toggleMenu: noAction, toggleList: noAction, toggleDetailMenu: noAction, setExpanded: noAction,
});
interface SessionLayoutProviderProps { userId: number; sessionPage?: boolean; children?: ReactNode }
const SessionLayoutState = ({ userId, sessionPage = false, children }: SessionLayoutProviderProps) => {
  const [menuPreference, setMenuPreference] = usePersistedState(`appearance:session-menu:${userId}`, false);
  // Keep both preferences mounted so navigation cannot hydrate a new key into the previous route's state.
  const [sessionMenuPreference, setSessionMenuPreference] = usePersistedState(`appearance:sessions-page-menu:${userId}`, true);
  const [listPreference, setListPreference] = usePersistedState(`appearance:session-list:${userId}`, false);
  const [expanded, setExpanded] = useState(false);
  // A session detail gets the width by folding the app menu; the stored preference
  // stays as the user left it, so leaving the detail restores the sidebar.
  const [detailMenuFold, setDetailMenuFold] = useState(false);
  const menuCollapsed = detailMenuFold || (sessionPage ? sessionMenuPreference : menuPreference) === true;
  const toggleMenu = () => {
    setDetailMenuFold(false);
    const setPreference = sessionPage ? setSessionMenuPreference : setMenuPreference;
    setPreference(!menuCollapsed);
  };
  const toggleList = () => setListPreference(listPreference !== true);
  const toggleDetailMenu = (folded: boolean) => setDetailMenuFold(folded);
  return <SessionLayoutContext value={{ menuCollapsed, listCollapsed: listPreference === true, expanded, toggleMenu, toggleList, toggleDetailMenu, setExpanded }}>{children}</SessionLayoutContext>;
};
const SessionLayoutProvider = ({ userId, sessionPage, children }: SessionLayoutProviderProps) =>
  <SessionLayoutState key={userId} userId={userId} sessionPage={sessionPage}>{children}</SessionLayoutState>;
const useSessionLayout = () => useContext(SessionLayoutContext);

export { SessionLayoutProvider, useSessionLayout };
