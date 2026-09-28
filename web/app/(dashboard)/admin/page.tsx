'use client';

import { usePersistedState } from '@/lib/use-persisted-state';
import { useAuth } from '@/components/common/auth-context';
import { cn } from '@/lib/utils';
import { ClientsTab } from '@/components/admin/clients-tab';
import { StorageTab } from '@/components/admin/storage-tab';
import { ExcludedTab } from '@/components/admin/excluded-tab';
import { OrganizationInsightsTab } from '@/components/admin/organization-insights-tab';
import { AITab } from '@/components/admin/ai-tab';
import { UnpricedModelsTab } from '@/components/admin/unpriced-models-tab';

type AdminTab = 'clients' | 'storage' | 'excluded' | 'unpriced' | 'insights' | 'ai';

const TAB_BASE = 'px-4 py-2 text-[13px] font-medium transition-colors border-b-2 -mb-px';
const TAB_ACTIVE = 'border-brand text-brand';
const TAB_INACTIVE = 'border-transparent text-ink-2 hover:text-ink';

export default function AdminPage() {
  const { isAdmin } = useAuth();
  const [activeTab, setActiveTab] = usePersistedState<AdminTab>('admin:activeTab', 'clients');

  const handleSelectClients = () => setActiveTab('clients');
  const handleSelectStorage = () => setActiveTab('storage');
  const handleSelectExcluded = () => setActiveTab('excluded');
  const handleSelectUnpriced = () => setActiveTab('unpriced');
  const handleSelectInsights = () => setActiveTab('insights');
  const handleSelectAI = () => setActiveTab('ai');

  if (!isAdmin) {
    return <div className="px-4 py-6 text-sm text-ink-3">관리자 전용 페이지입니다.</div>;
  }

  return (
    <div className="space-y-4">
      <header className="flex items-center justify-between">
        <h2 className="text-[16px] font-semibold text-ink">Admin</h2>
      </header>

      <nav className="flex gap-1 border-b border-border">
        <button
          onClick={handleSelectClients}
          className={cn(TAB_BASE, activeTab === 'clients' ? TAB_ACTIVE : TAB_INACTIVE)}
        >
          Clients
        </button>
        <button
          onClick={handleSelectStorage}
          className={cn(TAB_BASE, activeTab === 'storage' ? TAB_ACTIVE : TAB_INACTIVE)}
        >
          Storage
        </button>
        <button
          onClick={handleSelectExcluded}
          className={cn(TAB_BASE, activeTab === 'excluded' ? TAB_ACTIVE : TAB_INACTIVE)}
        >
          Excluded Accounts
        </button>
        <button
          onClick={handleSelectUnpriced}
          className={cn(TAB_BASE, activeTab === 'unpriced' ? TAB_ACTIVE : TAB_INACTIVE)}
        >
          Unpriced Models
        </button>
        <button
          onClick={handleSelectInsights}
          className={cn(TAB_BASE, activeTab === 'insights' ? TAB_ACTIVE : TAB_INACTIVE)}
        >
          Insights
        </button>
        <button
          onClick={handleSelectAI}
          className={cn(TAB_BASE, activeTab === 'ai' ? TAB_ACTIVE : TAB_INACTIVE)}
        >
          AI
        </button>
      </nav>

      {activeTab === 'clients' && <ClientsTab />}
      {activeTab === 'storage' && <StorageTab />}
      {activeTab === 'excluded' && <ExcludedTab />}
      {activeTab === 'unpriced' && <UnpricedModelsTab />}
      {activeTab === 'insights' && <OrganizationInsightsTab />}
      {activeTab === 'ai' && <AITab />}
    </div>
  );
}
