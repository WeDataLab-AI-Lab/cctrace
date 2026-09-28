'use client';

import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { resetUserPassword } from '@/lib/api';
import type { DashboardUserInfo } from '@/lib/types';
import { TempPasswordDialog } from './temp-password-dialog';

interface ResetPasswordDialogProps {
  user: DashboardUserInfo;
  onClose: () => void;
  onSuccess: () => void;
}

const ResetPasswordDialog = ({ user, onClose, onSuccess }: ResetPasswordDialogProps) => {
  const [tempPassword, setTempPassword] = useState<string | null>(null);
  const mutation = useMutation({
    mutationFn: () => resetUserPassword(user.id),
    onSuccess: (data) => {
      onSuccess();
      setTempPassword(data.temp_password);
    },
  });

  const handleOpenChange = (open: boolean) => {
    if (!open) onClose();
  };
  const handleSubmit = () => mutation.mutate();

  if (tempPassword) {
    return <TempPasswordDialog tempPassword={tempPassword} onClose={onClose} />;
  }

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[360px] max-w-[360px] gap-0 space-y-4 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <DialogTitle className="text-[15px] font-semibold text-ink">Reset Password</DialogTitle>
        <p className="text-xs text-ink-3">{user.email}</p>
        <p className="text-sm text-ink-2">A temporary password will be generated for this user. They will be prompted to change it on next login.</p>
        {mutation.isError && <p className="text-xs text-danger">Failed to reset password. Please try again.</p>}
        <div className="flex gap-2 pt-1">
          <Button
            onClick={handleSubmit}
            disabled={mutation.isPending}
            className="flex-1 h-auto py-2 text-sm font-semibold rounded-lg bg-brand text-brand-ink disabled:opacity-40 disabled:cursor-not-allowed hover:bg-brand-hover"
          >
            {mutation.isPending ? 'Resetting...' : 'Reset'}
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

export { ResetPasswordDialog };
export type { ResetPasswordDialogProps };
