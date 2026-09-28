'use client';

import { Suspense } from 'react';
import { usePersistedState } from '@/lib/use-persisted-state';
import { useAuth } from '@/components/common/auth-context';
import { cn } from '@/lib/utils';
import { AnalyticsTab } from '@/components/users/analytics-tab';
import { ManagementTab } from '@/components/users/management-tab';
import { CollectingLoader } from '@/components/common/collecting-loader';

const TAB_BASE = 'px-4 py-2 text-[13px] font-medium transition-colors border-b-2 -mb-px';
const TAB_ACTIVE = 'border-brand text-brand';
const TAB_INACTIVE = 'border-transparent text-ink-2 hover:text-ink';

export default function UsersPage() {
  const { isAdmin } = useAuth();
  const [activeTab, setActiveTab] = usePersistedState<'analytics' | 'management'>('users:activeTab', 'analytics');

  const handleSelectAnalytics = () => setActiveTab('analytics');
  const handleSelectManagement = () => setActiveTab('management');

  return (
    <div className="space-y-4">
      <header className="flex items-center justify-between">
        <h2 className="text-[16px] font-semibold text-ink">Users</h2>
      </header>

      <nav className="flex gap-1 border-b border-border">
        <button
          onClick={handleSelectAnalytics}
          className={cn(TAB_BASE, activeTab === 'analytics' ? TAB_ACTIVE : TAB_INACTIVE)}
        >
          Analytics
        </button>
        {isAdmin && (
          <button
            onClick={handleSelectManagement}
            className={cn(TAB_BASE, activeTab === 'management' ? TAB_ACTIVE : TAB_INACTIVE)}
          >
            Management
          </button>
        )}
      </nav>

      {activeTab === 'management' && isAdmin ? (
        <ManagementTab />
      ) : (
        <Suspense fallback={<CollectingLoader className="p-8" />}>
          <AnalyticsTab isAdmin={isAdmin} />
        </Suspense>
      )}
    </div>
  );
}
