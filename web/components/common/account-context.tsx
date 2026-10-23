'use client';

import { createContext, useContext, useState, useEffect, type ReactNode } from 'react';
import { ACCOUNT_KEY as STORAGE_KEY, FILTER_RESET_EVENT, OWN_ACCOUNTS_KEY } from '@/lib/filter-reset';

interface AccountContextType {
  selectedAccount: string; // '' means all
  setSelectedAccount: (account: string) => void;
  ownAccountsOnly: boolean;
  setOwnAccountsOnly: (on: boolean) => void;
  isHydrated: boolean;
}

const AccountContext = createContext<AccountContextType>({
  selectedAccount: '',
  setSelectedAccount: () => {},
  ownAccountsOnly: false,
  setOwnAccountsOnly: () => {},
  isHydrated: false,
});

const AccountProvider = ({ children }: { children: ReactNode }) => {
  const [selectedAccount, setSelectedAccountState] = useState('');
  const [ownAccountsOnly, setOwnAccountsOnlyState] = useState(false);
  const [isHydrated, setIsHydrated] = useState(false);

  const setSelectedAccount = (account: string) => {
    setSelectedAccountState(account);
    try { localStorage.setItem(STORAGE_KEY, account); } catch { /* ignore */ }
  };

  const setOwnAccountsOnly = (on: boolean) => {
    setOwnAccountsOnlyState(on);
    try { localStorage.setItem(OWN_ACCOUNTS_KEY, on ? '1' : '0'); } catch { /* ignore */ }
  };

  /** 마운트 후 localStorage에서 계정 선택과 "Mine"(내 계정만) 설정을 1회 복원. 정적 export 하이드레이션 불일치를 피하려 의도적으로 effect에서 setState. */
  useEffect(() => {
    try {
      const v = localStorage.getItem(STORAGE_KEY);
      // eslint-disable-next-line react-hooks/set-state-in-effect -- 의도된 post-mount 하이드레이션
      if (v !== null) setSelectedAccountState(v);
      setOwnAccountsOnlyState(localStorage.getItem(OWN_ACCOUNTS_KEY) === '1');
    } catch { /* ignore */ }
    setIsHydrated(true);
  }, []);

  /** 로고 클릭(명시적 초기화) 시 계정 범위를 전체로 되돌린다. 저장소는 이미 비워졌고, 여기서는 마운트된 상태의 값만 따라간다. */
  useEffect(() => {
    const onReset = () => setSelectedAccountState('');
    window.addEventListener(FILTER_RESET_EVENT, onReset);
    return () => window.removeEventListener(FILTER_RESET_EVENT, onReset);
  }, []);

  return (
    <AccountContext.Provider value={{ selectedAccount, setSelectedAccount, ownAccountsOnly, setOwnAccountsOnly, isHydrated }}>
      {children}
    </AccountContext.Provider>
  );
};

const useAccount = () => useContext(AccountContext);

export { AccountProvider, useAccount };
