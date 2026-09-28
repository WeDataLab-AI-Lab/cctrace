'use client';

import { createContext, useContext, useState, useEffect, type ReactNode } from 'react';
import { useIsRestoring, useQueryClient } from '@tanstack/react-query';
import { fetchSetupStatus, fetchMe, login as apiLogin, logout as apiLogout, refreshToken, setAuthFailureHandler } from '@/lib/api';
import type { AuthUser } from '@/lib/types';
import {
  reconcileWeeklyQueriesAfterAuth,
  WEEKLY_QUERY_STORAGE_KEY,
} from '@/lib/weekly-query';

interface AuthContextType {
  user: AuthUser | null;
  isAdmin: boolean;
  isLoading: boolean;
  needsSetup: boolean;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const isTestEnvironment = process.env.NEXT_PUBLIC_APP_ENV === 'test';
const testUser: AuthUser = {
  id: 1,
  email: 'test@cctrace.dev',
  role: 'admin',
  name: 'Test User',
  must_change_password: false,
};

const AuthContext = createContext<AuthContextType>({
  user: null,
  isAdmin: false,
  isLoading: true,
  needsSetup: false,
  login: async () => {},
  logout: async () => {},
});

const AuthProvider = ({ children }: { children: ReactNode }) => {
  const [user, setUser] = useState<AuthUser | null>(isTestEnvironment ? testUser : null);
  const [isLoading, setIsLoading] = useState(!isTestEnvironment);
  const [needsSetup, setNeedsSetup] = useState(false);
  const queryClient = useQueryClient();
  const isRestoring = useIsRestoring();

  const handleLogin = async (email: string, password: string) => {
    const result = await apiLogin(email, password);
    setUser(result.user);
    setNeedsSetup(false);
  };

  const handleLogout = async () => {
    await apiLogout();
    setUser(null);
  };

  /**
   * 마운트 시 setup 필요 여부 확인 → 현재 사용자 조회(실패 시 refresh 시도).
   * 401+refresh 모두 실패하면 user를 비워 대시보드가 로그인으로 라우팅되게 한다.
   */
  useEffect(() => {
    if (isTestEnvironment) return;

    let cancelled = false;
    const init = async () => {
      try {
        const status = await fetchSetupStatus();
        if (cancelled) return;
        if (status.needs_setup) {
          setNeedsSetup(true);
          setIsLoading(false);
          return;
        }
        const me = await fetchMe();
        if (cancelled) return;
        setUser(me);
      } catch {
        try {
          const result = await refreshToken();
          if (cancelled) return;
          setUser(result.user);
        } catch {
          if (cancelled) return;
          setUser(null);
        }
      } finally {
        if (!cancelled) setIsLoading(false);
      }
    };
    init();

    setAuthFailureHandler(() => {
      if (!cancelled) setUser(null);
    });

    return () => {
      cancelled = true;
      setAuthFailureHandler(null);
    };
  }, []);

  /**
   * Once authentication and persisted restoration both resolve, remove weekly
   * data owned by every other user. Waiting for both closes either completion order.
   * The persisted slot is removed in the same transition; the persistence
   * subscription then rewrites it from the already-filtered active cache.
   */
  useEffect(() => {
    const removedForeignData = reconcileWeeklyQueriesAfterAuth(queryClient, {
      authenticatedUserId: user?.id ?? null,
      authResolved: !isLoading,
      restoreComplete: !isRestoring,
    });
    if (!removedForeignData) return;

    try {
      localStorage.removeItem(WEEKLY_QUERY_STORAGE_KEY);
    } catch (error) {
      if (!(error instanceof DOMException)) throw error;
    }
  }, [isLoading, isRestoring, queryClient, user?.id]);

  return (
    <AuthContext.Provider
      value={{
        user,
        isAdmin: user?.role === 'admin',
        isLoading,
        needsSetup,
        login: handleLogin,
        logout: handleLogout,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
};

const useAuth = () => useContext(AuthContext);

export { AuthProvider, useAuth };
