'use client';

import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { listDashboardUsers, updateDashboardUser } from '@/lib/api';
import { cn } from '@/lib/utils';
import { CollectingLoader } from '@/components/common/collecting-loader';
import type { DashboardUserInfo } from '@/lib/types';
import { POLL_NORMAL } from '@/lib/query-config';
import { AddUserDialog } from './add-user-dialog';
import { ClearDataDialog } from './clear-data-dialog';
import { EditUserDialog } from './edit-user-dialog';
import { ResetPasswordDialog } from './reset-password-dialog';
import { RevokeTokenDialog } from './revoke-token-dialog';
import { UserActionsMenu } from './user-actions-menu';

const GRID_CLASS = 'grid grid-cols-[1fr_1fr_100px_100px_80px_140px_100px_48px] items-center';

const ManagementTab = () => {
  const queryClient = useQueryClient();
  const [addOpen, setAddOpen] = useState(false);
  const [editUser, setEditUser] = useState<DashboardUserInfo | null>(null);
  const [resetUser, setResetUser] = useState<DashboardUserInfo | null>(null);
  const [revokeUser, setRevokeUser] = useState<DashboardUserInfo | null>(null);
  const [clearDataUser, setClearDataUser] = useState<DashboardUserInfo | null>(null);

  const { data: users = [], isLoading } = useQuery<DashboardUserInfo[]>({
    queryKey: ['dashboard-users'],
    queryFn: listDashboardUsers,
    refetchInterval: POLL_NORMAL,
  });

  const toggleActive = useMutation({
    mutationFn: (u: DashboardUserInfo) => updateDashboardUser(u.id, { is_active: !u.is_active }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['dashboard-users'] }),
  });

  const refresh = () => queryClient.invalidateQueries({ queryKey: ['dashboard-users'] });

  const handleAddOpen = () => setAddOpen(true);
  const handleAddClose = () => setAddOpen(false);
  const handleEditClose = () => setEditUser(null);
  const handleResetClose = () => setResetUser(null);
  const handleRevokeClose = () => setRevokeUser(null);
  const handleClearDataClose = () => setClearDataUser(null);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-ink-2">{users.length} users</span>
        <Button
          onClick={handleAddOpen}
          className="h-auto gap-1.5 px-3 py-1.5 text-[13px] font-medium rounded-lg bg-brand text-brand-ink hover:bg-brand-hover"
        >
          <Plus size={14} /> Add User
        </Button>
      </div>

      <div className="bg-surface border border-border rounded-lg overflow-visible">
        <header className={cn(GRID_CLASS, 'px-4 py-2.5 border-b border-surface-sunk bg-canvas')}>
          <span className="text-[11px] font-medium text-ink-3">Email</span>
          <span className="text-[11px] font-medium text-ink-3">Name</span>
          <span className="text-[11px] font-medium text-ink-3">Team</span>
          <span className="text-[11px] font-medium text-ink-3">Role</span>
          <span className="text-[11px] font-medium text-ink-3">Status</span>
          <span className="text-[11px] font-medium text-ink-3">cctrace User ID</span>
          <span className="text-[11px] font-medium text-ink-3">Created</span>
          <span />
        </header>

        {isLoading && <CollectingLoader className="py-6" />}

        {!isLoading && users.length === 0 && (
          <div className="px-4 py-6 text-center text-sm text-ink-3">No users</div>
        )}

        {users.map((u) => (
          <div
            key={u.id}
            className={cn(GRID_CLASS, 'px-4 py-3 border-b border-surface-sunk last:border-b-0 hover:bg-canvas transition-colors')}
          >
            <span className="text-[13px] text-ink truncate pr-2">{u.email}</span>
            <span className="text-[13px] text-ink-2 truncate pr-2">{u.name || '—'}</span>
            <span className="text-[12px] text-ink-2 truncate pr-2">{u.team || '—'}</span>
            <span className={cn('text-[12px] font-medium', u.role === 'admin' ? 'text-brand' : 'text-ink-2')}>
              {u.role}
            </span>
            <span>
              <Badge
                className={cn(
                  'rounded-full px-2 py-0.5 text-[11px] font-medium',
                  u.is_active ? 'bg-success-soft text-success-strong' : 'bg-surface-sunk text-ink-3',
                )}
              >
                {u.is_active ? 'active' : 'inactive'}
              </Badge>
            </span>
            <span className="pr-2">
              {u.cctrace_user_id ? (
                <span className="text-[12px] text-ink font-mono truncate block">{u.cctrace_user_id}</span>
              ) : (
                <Badge className="rounded-full px-2 py-0.5 text-[11px] font-medium bg-warning-soft text-warning-strong border border-warning/40">
                  not set
                </Badge>
              )}
            </span>
            <span className="text-[12px] text-ink-3">{new Date(u.created_at).toLocaleDateString()}</span>
            <UserActionsMenu
              user={u}
              onEdit={() => setEditUser(u)}
              onToggleActive={() => toggleActive.mutate(u)}
              onResetPassword={() => setResetUser(u)}
              onRevokeToken={() => setRevokeUser(u)}
              onClearData={() => setClearDataUser(u)}
            />
          </div>
        ))}
      </div>

      {addOpen && <AddUserDialog onClose={handleAddClose} onSuccess={refresh} />}
      {editUser && <EditUserDialog user={editUser} onClose={handleEditClose} onSuccess={refresh} />}
      {resetUser && <ResetPasswordDialog user={resetUser} onClose={handleResetClose} onSuccess={refresh} />}
      {revokeUser && <RevokeTokenDialog user={revokeUser} onClose={handleRevokeClose} onSuccess={refresh} />}
      {clearDataUser && <ClearDataDialog user={clearDataUser} onClose={handleClearDataClose} onSuccess={refresh} />}
    </div>
  );
};

export { ManagementTab };
