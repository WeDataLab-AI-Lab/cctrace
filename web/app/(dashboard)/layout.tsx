'use client';

import { useEffect } from 'react';
import { useRouter, usePathname } from 'next/navigation';
import { Sidebar } from '@/components/common/sidebar';
import { Header } from '@/components/common/header';
import { useAuth } from '@/components/common/auth-context';
import { CollectingLoader } from '@/components/common/collecting-loader';

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const { user, isLoading, needsSetup } = useAuth();
  const router = useRouter();
  const pathname = usePathname();

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
      <div className="flex h-screen items-center justify-center bg-canvas">
        <CollectingLoader />
      </div>
    );
  }

  if (!user) {
    return null;
  }

  return (
    <div className="flex h-screen overflow-hidden bg-canvas">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <Header />
        {/* min-w-0 as well as min-h-0: a flex child defaults to min-width:auto, so
            without it main sizes to its widest descendant and the page grows a
            horizontal scrollbar instead of its contents narrowing. Every shrink rule
            further in -- the session split, the filter toolbar -- is inert until the
            container agrees to be narrower than its contents. */}
        <main className="min-h-0 min-w-0 flex-1 overflow-auto p-4 md:p-8">
          {children}
        </main>
      </div>
    </div>
  );
}
