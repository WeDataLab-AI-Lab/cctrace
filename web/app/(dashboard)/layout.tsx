'use client';

import { useEffect, useRef } from 'react';
import { useRouter, usePathname } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { Sidebar } from '@/components/common/sidebar';
import { Header } from '@/components/common/header';
import { useAuth } from '@/components/common/auth-context';
import { CollectingLoader } from '@/components/common/collecting-loader';
import { CatchingUpNote } from '@/components/common/catching-up-note';
import { DemoBanner } from '@/components/common/demo-banner';
import { fetchMaintenanceStatus } from '@/lib/api';
import { POLL_LIVE } from '@/lib/query-config';
import { TOKEN_RULE_NOTE } from '@/lib/token-rule';
import { cn } from '@/lib/utils';
import { useRefreshWhenRebuilt } from '@/lib/use-refresh-when-rebuilt';
import { SessionLayoutProvider, useSessionLayout } from '@/components/common/session-layout-context';

const DashboardChrome = ({ children, compatibilityRoute, pending }: { children: React.ReactNode; compatibilityRoute: boolean; pending?: boolean }) => {
  const { menuCollapsed, expanded } = useSessionLayout();
  const pathname = usePathname();
  const mainRef = useRef<HTMLElement>(null);
  const hideChrome = expanded || compatibilityRoute;
  useEffect(() => {
    if (!mainRef.current) return;
    mainRef.current.scrollTop = 0;
    mainRef.current.scrollLeft = 0;
  }, [pathname]);
  return <div className="flex h-dvh min-h-0 overflow-hidden bg-canvas">
    <div hidden={hideChrome} className={hideChrome ? 'hidden' : 'contents'}><Sidebar collapsed={menuCollapsed} /></div>
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div hidden={hideChrome} className={hideChrome ? 'hidden' : 'contents'}><Header /></div>
      <main ref={mainRef} className={cn('flex min-h-0 min-w-0 flex-1 flex-col', hideChrome ? 'overflow-hidden' : 'mx-auto w-full max-w-[1600px] overflow-auto overscroll-contain p-4 md:p-8')}>
        {process.env.NEXT_PUBLIC_DEMO === '1' && !hideChrome && <DemoBanner className="mb-4" />}
        {!hideChrome && <CatchingUpNote pending={pending} message={TOKEN_RULE_NOTE} className="mb-4 shrink-0" />}
        <div className="min-h-0 min-w-0 flex-1">{children}</div>
      </main>
    </div>
  </div>;
};

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const { user, isLoading, needsSetup } = useAuth();
  const router = useRouter();
  const pathname = usePathname();
  const dashboardPath = pathname?.replace(/\/+$/, '');
  const analysisWorkspace = dashboardPath === '/sessions/analysis';
  const sessionPage = dashboardPath === '/sessions';

  // While the server recounts token totals under the new counting rule, screens
  // that read its stored aggregates and screens that count live can disagree, so
  // every page says so. Polled only while that is pending. A failed read keeps
  // the last answer -- react-query keeps data through an error -- so it neither
  // hides the note nor reads as finished; before the first answer nothing shows.
  // When the recount finishes, every query refetches.
  const { data: maintenance } = useQuery({
    queryKey: ['maintenance-status'],
    queryFn: fetchMaintenanceStatus,
    enabled: Boolean(user),
    refetchInterval: (query) => (query.state.data?.token_rule_pending === true ? POLL_LIVE : false),
  });
  useRefreshWhenRebuilt(maintenance?.token_rule_pending);

  /** 인증과 초기 암호 상태에 따라 접근 가능한 페이지로 이동한다. */
  useEffect(() => {
    if (isLoading) return;
    if (needsSetup) {
      router.replace('/setup');
    } else if (!user) {
      router.replace('/login');
    } else if (user.must_change_password && pathname !== '/settings') {
      router.replace('/settings');
    }
  }, [user, isLoading, needsSetup, pathname, router]);

  if (isLoading) {
    return (
      <div className="flex h-dvh items-center justify-center bg-canvas">
        <CollectingLoader />
      </div>
    );
  }

  if (!user) {
    return null;
  }

  return (
    <SessionLayoutProvider userId={user.id} sessionPage={sessionPage}>
      <DashboardChrome compatibilityRoute={analysisWorkspace} pending={maintenance?.token_rule_pending}>{children}</DashboardChrome>
    </SessionLayoutProvider>
  );
}
