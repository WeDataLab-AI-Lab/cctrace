'use client';

import { useMutation } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { deleteUserData } from '@/lib/api';
import type { DashboardUserInfo } from '@/lib/types';

interface ClearDataDialogProps {
  user: DashboardUserInfo;
  onClose: () => void;
  onSuccess: () => void;
}

const ClearDataDialog = ({ user, onClose, onSuccess }: ClearDataDialogProps) => {
  const mutation = useMutation({
    mutationFn: () => deleteUserData(user.email, user.cctrace_user_id),
    onSuccess: () => {
      onSuccess();
      onClose();
    },
  });

  const handleOpenChange = (open: boolean) => {
    if (!open) onClose();
  };
  const handleSubmit = () => mutation.mutate();

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[360px] max-w-[360px] gap-0 space-y-4 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <DialogTitle className="text-[15px] font-semibold text-ink">Clear Collected Data</DialogTitle>
        <p className="text-xs text-ink-3">{user.email} ({user.cctrace_user_id})</p>
        <p className="text-sm text-ink-2">This will permanently delete all OTEL events, metrics, and session records for this user. This action cannot be undone.</p>
        {mutation.isError && <p className="text-xs text-danger">Failed to clear data. Please try again.</p>}
        <div className="flex gap-2 pt-1">
          <Button
            onClick={handleSubmit}
            disabled={mutation.isPending}
            className="flex-1 h-auto py-2 text-sm font-semibold rounded-lg bg-danger text-white disabled:opacity-40 disabled:cursor-not-allowed hover:bg-danger/90"
          >
            {mutation.isPending ? 'Clearing...' : 'Clear All Data'}
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

export { ClearDataDialog };
export type { ClearDataDialogProps };
