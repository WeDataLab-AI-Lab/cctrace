'use client';

import { Fragment, useState } from 'react';
import { useQuery, useQueryClient, keepPreviousData } from '@tanstack/react-query';
import { useSearchParams } from 'next/navigation';
import { POLL_NORMAL } from '@/lib/query-config';
import { usePersistedState } from '@/lib/use-persisted-state';
import { fetchCostByModel, fetchCostByUser, fetchUserNameMap } from '@/lib/api';
import { matchCategory } from '@/lib/colors';
import type { ModelCategory } from '@/lib/colors';
import { FilterBar } from '@/components/common/filter-bar';
import { CollectingLoader } from '@/components/common/collecting-loader';
import type { ViewMode } from '@/components/common/trend-chart';
import { useAccount } from '@/components/common/account-context';
import { usePendingAction, type PendingAction } from '@/components/common/pending-action-context';
import { Input } from '@/components/ui/input';
import type { CostSummary, ModelStat } from '@/lib/types';
import { AVATAR_COLORS, daysAgo, resolveDisplayName, userKey } from './user-utils';
import { UserCard, type UserCardStats } from './user-card';
import { UserDetail } from './user-detail';
import { MergeDialog } from './merge-dialog';
import { DeleteDialog } from './delete-dialog';

interface AnalyticsTabProps {
  isAdmin: boolean;
}

type ZeroStats = Pick<CostSummary, 'total_cost' | 'total_input_tokens' | 'total_output_tokens' | 'request_count'>;

const AnalyticsTab = ({ isAdmin }: AnalyticsTabProps) => {
  const searchParams = useSearchParams();
  const initialUser = searchParams.get('user');
  const [selected, setSelected] = useState<string | null>();
  const [search, setSearch] = useState('');
  const [hoveredKey, setHoveredKey] = useState<string | null>(null);
  const [dragKey, setDragKey] = useState<string | null>(null);
  const [dropKey, setDropKey] = useState<string | null>(null);
  const [mergeModal, setMergeModal] = useState<{ from: CostSummary; to: CostSummary } | null>(null);
  const [deleteModal, setDeleteModal] = useState<CostSummary | null>(null);
  const [viewMode, setViewMode] = usePersistedState<ViewMode>('filter:viewMode', 'cost');
  const [modelCategory, setModelCategory] = usePersistedState<ModelCategory>('filter:modelCategory', 'all');
  const [groupBy, setGroupBy] = usePersistedState<'model' | 'user'>('filter:groupBy', 'model');
  const [modelFilter, setModelFilter] = usePersistedState('filter:modelFilter', '__all__');
  const queryClient = useQueryClient();
  const { pendingAction, setPendingAction } = usePendingAction();
  // Date.now() 기반 값은 매 렌더 변동(=queryKey 흔들림→refetch 루프)을 막기 위해 1회만 계산.
  const [since] = useState(() => daysAgo(30));
  const { selectedAccount } = useAccount();

  const { data, isLoading } = useQuery<CostSummary[]>({
    queryKey: ['cost-by-user', since, selectedAccount],
    queryFn: () => fetchCostByUser(since, undefined, undefined, selectedAccount || undefined),
    // A refresh mid-drag re-sorts the cards under the cursor and the drop lands on the
    // wrong user. Hold the poll until the drag ends; the next tick picks it up.
    refetchInterval: dragKey ? false : POLL_NORMAL,
    // The account scope is part of the queryKey; keep the card list on screen while the
    // new scope loads instead of collapsing to "No data".
    placeholderData: keepPreviousData,
  });

  const { data: pageModelData = [] } = useQuery<ModelStat[]>({
    queryKey: ['cost-by-model', since, selectedAccount],
    queryFn: () => fetchCostByModel(since, undefined, undefined, selectedAccount || undefined),
    staleTime: Infinity,
    // Same drag gate as cost-by-user above: a refresh mid-drag must not disturb the cards.
    refetchInterval: dragKey ? false : POLL_NORMAL,
  });

  const { data: nameMap = {} } = useQuery<Record<string, string>>({
    queryKey: ['user-name-map'],
    queryFn: fetchUserNameMap,
    staleTime: Infinity,
  });

  const initialSelection = initialUser
    ? data?.find((user) => user.profile_email === initialUser || user.user_id === initialUser)
    : undefined;
  const selectedKey = selected === undefined && initialSelection ? userKey(initialSelection) : selected;

  const modelSeen = new Map<string, number>();
  for (const m of pageModelData) {
    const short = m.model.replace('claude-', '').split('-2025')[0].split('-2024')[0];
    if (short) modelSeen.set(short, (modelSeen.get(short) || 0) + m.total_cost);
  }
  const models = [...modelSeen.entries()]
    .filter(([, v]) => v > 0)
    .sort((a, b) => b[1] - a[1])
    .map(([m]) => m);

  // Stable user list (unfiltered) — order never changes on filter toggle
  let baseItems = data ?? [];
  // Optimistic: remap 'from' entries to 'to' user so merge appears applied
  if (pendingAction?.type === 'merge') {
    const fromKey = `${pendingAction.from.profile_email}|${pendingAction.from.user_id}`;
    baseItems = baseItems.map((c) => {
      const key = `${c.profile_email}|${c.user_id}`;
      if (key === fromKey) {
        return { ...c, profile_email: pendingAction.to.profile_email, user_id: pendingAction.to.user_id };
      }
      return c;
    });
  }

  const userMap = baseItems.reduce<Record<string, CostSummary>>((acc, c) => {
    const key = `${c.profile_email}|${c.user_id}`;
    if (!acc[key]) {
      acc[key] = { ...c, login_emails: [...(c.login_emails ?? [])] };
    } else {
      acc[key].total_cost += c.total_cost;
      acc[key].total_input_tokens += c.total_input_tokens;
      acc[key].total_output_tokens += c.total_output_tokens;
      acc[key].request_count += c.request_count;
      const existing = new Set(acc[key].login_emails ?? []);
      (c.login_emails ?? []).forEach((e) => existing.add(e));
      acc[key].login_emails = [...existing];
    }
    return acc;
  }, {});
  const sorted = Object.values(userMap).sort((a, b) => b.total_cost - a.total_cost);

  // Filtered stats per user (for display numbers only — list stays stable)
  const buildDisplayStats = (): Map<string, ZeroStats> | null => {
    if (modelCategory === 'all') return null; // use sorted data as-is
    const items = data ?? [];
    const zero: ZeroStats = { total_cost: 0, total_input_tokens: 0, total_output_tokens: 0, request_count: 0 };
    const map = new Map<string, ZeroStats>();
    for (const u of sorted) map.set(userKey(u), { ...zero });
    for (const c of items) {
      if (!matchCategory(c.agent, c.billing_provider, modelCategory)) continue;
      // Find the sorted user this row belongs to (match by profile_email + user_id, accounting for merges)
      let key = `${c.profile_email}|${c.user_id}`;
      if (pendingAction?.type === 'merge') {
        const fromKey = `${pendingAction.from.profile_email}|${pendingAction.from.user_id}`;
        if (key === fromKey) key = `${pendingAction.to.profile_email}|${pendingAction.to.user_id}`;
      }
      const entry = map.get(key);
      if (entry) {
        entry.total_cost += c.total_cost;
        entry.total_input_tokens += c.total_input_tokens;
        entry.total_output_tokens += c.total_output_tokens;
        entry.request_count += c.request_count;
      }
    }
    return map;
  };
  const displayStats = buildDisplayStats();

  const filtered = (() => {
    if (!search.trim()) return sorted;
    const q = search.toLowerCase();
    return sorted.filter(
      (u) =>
        (u.user_id ?? '').toLowerCase().includes(q) ||
        u.profile_email.toLowerCase().includes(q) ||
        (u.login_emails ?? []).some((e) => e.toLowerCase().includes(q)),
    );
  })();

  const handleSearchChange = (e: React.ChangeEvent<HTMLInputElement>) => setSearch(e.target.value);
  const handleRefresh = () => queryClient.invalidateQueries();
  const handleMergeConfirm = () => {
    if (!mergeModal) return;
    const action: PendingAction = { type: 'merge', from: mergeModal.from, to: mergeModal.to };
    setMergeModal(null);
    setPendingAction(action);
  };
  const handleMergeCancel = () => setMergeModal(null);
  const handleDeleteConfirm = () => {
    if (!deleteModal) return;
    const action: PendingAction = { type: 'delete', user: deleteModal };
    setDeleteModal(null);
    setPendingAction(action);
  };
  const handleDeleteCancel = () => setDeleteModal(null);

  return (
    <div className="space-y-4">
      <Input
        type="text"
        value={search}
        onChange={handleSearchChange}
        placeholder="Search by user ID, email..."
        className="h-auto w-full rounded-lg border-border bg-surface px-3 py-2 text-[13px] shadow-none placeholder:text-ink-4 focus-visible:ring-0 focus-visible:border-brand"
      />

      <FilterBar
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        modelCategory={modelCategory}
        onModelCategoryChange={setModelCategory}
        groupBy={groupBy}
        onGroupByChange={setGroupBy}
        showGroupBy={false}
        modelFilter={modelFilter}
        onModelFilterChange={setModelFilter}
        models={models}
        showModelFilter={true}
        onRefresh={handleRefresh}
      />

      {isLoading && <CollectingLoader />}

      <div className="space-y-3">
        {filtered.map((user, index) => {
          const key = userKey(user);
          const isSelected = selectedKey === key;
          const dashName = user.user_id ? nameMap[user.user_id] : undefined;
          const avatarColor = AVATAR_COLORS[index % AVATAR_COLORS.length];
          const stats: UserCardStats = displayStats?.get(key) ?? user;
          const canAcceptDrop = !!dragKey && dragKey !== key;

          return (
            <Fragment key={key}>
              <UserCard
                user={user}
                avatarColor={avatarColor}
                dashName={dashName}
                stats={stats}
                viewMode={viewMode}
                isAdmin={isAdmin}
                isSelected={isSelected}
                isDragging={dragKey === key}
                isDropTarget={dropKey === key}
                isHovered={hoveredKey === key}
                canAcceptDrop={canAcceptDrop}
                onToggleSelect={() => setSelected(isSelected ? null : key)}
                onHoverChange={(hovered) => setHoveredKey(hovered ? key : null)}
                onDragStart={() => setDragKey(key)}
                onDragEnd={() => {
                  setDragKey(null);
                  setDropKey(null);
                }}
                onDragEnter={() => setDropKey(key)}
                onDragLeave={() => setDropKey(null)}
                onDrop={() => {
                  const fromUser = sorted.find((u) => userKey(u) === dragKey);
                  const toUser = sorted.find((u) => userKey(u) === key);
                  if (fromUser && toUser) setMergeModal({ from: fromUser, to: toUser });
                  setDragKey(null);
                  setDropKey(null);
                }}
                onDelete={() => setDeleteModal(user)}
              />
              {isSelected && (
                <UserDetail
                  email={user.profile_email}
                  avatarColor={avatarColor}
                  displayName={resolveDisplayName(user).shortName}
                  viewMode={viewMode}
                  filterAccount={selectedAccount || undefined}
                  modelFilter={modelFilter}
                  modelCategory={modelCategory}
                  userId={user.user_id || undefined}
                />
              )}
            </Fragment>
          );
        })}
      </div>

      {!isLoading && filtered.length === 0 && (
        <div className="bg-surface rounded-lg border border-border p-8 text-center text-ink-3">No data</div>
      )}

      {isAdmin && sorted.length > 1 && (
        <p className="text-[11px] text-ink-4 text-center">
          사용자를 다른 사용자 위로 드래그하면 데이터를 병합할 수 있습니다
        </p>
      )}

      {mergeModal && (
        <MergeDialog from={mergeModal.from} to={mergeModal.to} onConfirm={handleMergeConfirm} onCancel={handleMergeCancel} />
      )}

      {deleteModal && <DeleteDialog user={deleteModal} onConfirm={handleDeleteConfirm} onCancel={handleDeleteCancel} />}
    </div>
  );
};

export { AnalyticsTab };
export type { AnalyticsTabProps };
