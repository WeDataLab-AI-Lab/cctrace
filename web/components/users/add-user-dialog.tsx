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
import { createDashboardUser } from '@/lib/api';
import type { CreateUserRequest } from '@/lib/types';
import { fetchOtelUserIDs } from './otel-user-ids';
import { TempPasswordDialog } from './temp-password-dialog';

interface AddUserDialogProps {
  onClose: () => void;
  onSuccess: () => void;
}

const FIELD_CLASS =
  'h-auto rounded-lg border-border px-3 py-2 text-sm shadow-none focus-visible:ring-0 focus-visible:border-brand';

const AddUserDialog = ({ onClose, onSuccess }: AddUserDialogProps) => {
  const [form, setForm] = useState<CreateUserRequest>({ email: '', name: '', team: '', role: 'user', cctrace_user_id: '' });
  const [tempPassword, setTempPassword] = useState<string | null>(null);
  const { data: otelUserIDs = [] } = useQuery<string[]>({
    queryKey: ['otel-user-ids'],
    queryFn: fetchOtelUserIDs,
  });
  const mutation = useMutation({
    mutationFn: () => createDashboardUser(form),
    onSuccess: (data) => {
      onSuccess();
      if (data.temp_password) {
        setTempPassword(data.temp_password);
      } else {
        onClose();
      }
    },
  });

  const handleOpenChange = (open: boolean) => {
    if (!open) onClose();
  };
  const handleEmailChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, email: e.target.value }));
  const handleNameChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, name: e.target.value }));
  const handleRoleChange = (value: string) =>
    setForm((f) => ({ ...f, role: value as 'admin' | 'user' }));
  const handleTeamChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, team: e.target.value }));
  const handleUserIdChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, cctrace_user_id: e.target.value }));
  const handleSubmit = () => mutation.mutate();

  if (tempPassword) {
    return <TempPasswordDialog tempPassword={tempPassword} onClose={onClose} />;
  }

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[400px] max-w-[400px] gap-0 space-y-4 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <DialogTitle className="text-[15px] font-semibold text-ink">Add User</DialogTitle>
        <div className="space-y-3">
          <div>
            <Label className="block text-[11px] text-ink-2 mb-1">Email</Label>
            <Input type="email" value={form.email} onChange={handleEmailChange} className={FIELD_CLASS} />
          </div>
          <div>
            <Label className="block text-[11px] text-ink-2 mb-1">Name</Label>
            <Input type="text" value={form.name} onChange={handleNameChange} className={FIELD_CLASS} />
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
            <Label className="block text-[11px] text-ink-2 mb-1">Team</Label>
            <Input type="text" value={form.team} onChange={handleTeamChange} className={FIELD_CLASS} />
          </div>
          <div>
            <Label className="block text-[11px] text-ink-2 mb-1">cctrace User ID</Label>
            <Input
              type="text"
              list="otel-user-ids-add"
              value={form.cctrace_user_id ?? ''}
              onChange={handleUserIdChange}
              placeholder="Select or type a user ID"
              className={FIELD_CLASS}
            />
            <datalist id="otel-user-ids-add">
              {otelUserIDs.map((id) => (
                <option key={id} value={id} />
              ))}
            </datalist>
          </div>
        </div>
        {mutation.isError && <p className="text-xs text-danger">Failed to create user. Please try again.</p>}
        <div className="flex gap-2 pt-1">
          <Button
            onClick={handleSubmit}
            disabled={mutation.isPending || !form.email}
            className="flex-1 h-auto py-2 text-sm font-semibold rounded-lg bg-brand text-brand-ink disabled:opacity-40 disabled:cursor-not-allowed hover:bg-brand-hover"
          >
            {mutation.isPending ? 'Creating...' : 'Add User'}
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

export { AddUserDialog };
export type { AddUserDialogProps };
