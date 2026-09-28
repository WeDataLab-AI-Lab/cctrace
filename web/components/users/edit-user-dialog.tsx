'use client';

import { useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { updateDashboardUser } from '@/lib/api';
import type { DashboardUserInfo, UpdateUserRequest } from '@/lib/types';
import { fetchOtelUserIDs } from './otel-user-ids';

interface EditUserDialogProps {
  user: DashboardUserInfo;
  onClose: () => void;
  onSuccess: () => void;
}

const FIELD_CLASS =
  'h-auto rounded-lg border-border px-3 py-2 text-sm shadow-none focus-visible:ring-0 focus-visible:border-brand';

const EditUserDialog = ({ user, onClose, onSuccess }: EditUserDialogProps) => {
  const [form, setForm] = useState<UpdateUserRequest>({
    name: user.name,
    team: user.team ?? '',
    role: user.role,
    cctrace_user_id: user.cctrace_user_id ?? '',
  });
  const { data: otelUserIDs = [] } = useQuery<string[]>({
    queryKey: ['otel-user-ids'],
    queryFn: fetchOtelUserIDs,
  });
  // The server refuses an empty team, and `cctrace init` needs one: a team is
  // sent only when it changed, trimmed, and clearing an existing one blocks Save.
  const originalTeam = (user.team ?? '').trim();
  const team = (form.team ?? '').trim();
  const teamCleared = team === '' && originalTeam !== '';
  const buildRequest = (): UpdateUserRequest => ({
    ...form,
    team: team === originalTeam ? undefined : team,
  });
  const mutation = useMutation({
    mutationFn: () => updateDashboardUser(user.id, buildRequest()),
    onSuccess: () => {
      onSuccess();
      onClose();
    },
  });

  const handleOpenChange = (open: boolean) => {
    if (!open) onClose();
  };
  const handleNameChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, name: e.target.value }));
  const handleTeamChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, team: e.target.value }));
  const handleRoleChange = (value: string) =>
    setForm((f) => ({ ...f, role: value as 'admin' | 'user' }));
  const handleUserIdChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, cctrace_user_id: e.target.value }));
  const handleSubmit = () => mutation.mutate();

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[380px] max-w-[380px] gap-0 space-y-4 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <DialogTitle className="text-[15px] font-semibold text-ink">Edit User</DialogTitle>
        <p className="text-xs text-ink-3">{user.email}</p>
        <div className="space-y-3">
          <div>
            <Label htmlFor="edit-user-name" className="block text-[11px] text-ink-2 mb-1">
              Name
            </Label>
            <Input
              id="edit-user-name"
              type="text"
              value={form.name ?? ''}
              onChange={handleNameChange}
              className={FIELD_CLASS}
            />
          </div>
          <div>
            <Label htmlFor="edit-user-team" className="block text-[11px] text-ink-2 mb-1">
              Team
            </Label>
            <Input
              id="edit-user-team"
              type="text"
              value={form.team ?? ''}
              onChange={handleTeamChange}
              className={FIELD_CLASS}
            />
            {teamCleared && <p className="mt-1 text-xs text-danger">Team is required.</p>}
          </div>
          <div>
            <Label className="block text-[11px] text-ink-2 mb-1">Role</Label>
            <Select value={form.role} onValueChange={handleRoleChange}>
              <SelectTrigger className="w-full h-auto rounded-lg border-border bg-surface px-3 py-2 text-sm shadow-none focus-visible:ring-0 focus-visible:border-brand">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">User</SelectItem>
                <SelectItem value="admin">Admin</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div>
            <Label className="block text-[11px] text-ink-2 mb-1">cctrace User ID</Label>
            <Input
              type="text"
              list="otel-user-ids-edit"
              value={form.cctrace_user_id ?? ''}
              onChange={handleUserIdChange}
              placeholder="Select or type a user ID"
              className={FIELD_CLASS}
            />
            <datalist id="otel-user-ids-edit">
              {otelUserIDs.map((id) => (
                <option key={id} value={id} />
              ))}
            </datalist>
          </div>
        </div>
        {mutation.isError && <p className="text-xs text-danger">Failed to update user. Please try again.</p>}
        <div className="flex gap-2 pt-1">
          <Button
            onClick={handleSubmit}
            disabled={mutation.isPending || teamCleared}
            className="flex-1 h-auto py-2 text-sm font-semibold rounded-lg bg-brand text-brand-ink disabled:opacity-40 disabled:cursor-not-allowed hover:bg-brand-hover"
          >
            {mutation.isPending ? 'Saving...' : 'Save'}
          </Button>
          <Button
            variant="outline"
            onClick={onClose}
            className="flex-1 h-auto py-2 text-sm font-medium rounded-lg border-border text-ink-2 shadow-none hover:bg-canvas"
          >
            Cancel
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
};

export { EditUserDialog };
export type { EditUserDialogProps };
