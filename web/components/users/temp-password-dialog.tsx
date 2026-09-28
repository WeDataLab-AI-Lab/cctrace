'use client';

import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog';

interface TempPasswordDialogProps {
  tempPassword: string;
  onClose: () => void;
}

const TempPasswordDialog = ({ tempPassword, onClose }: TempPasswordDialogProps) => {
  const handleOpenChange = (open: boolean) => {
    if (!open) onClose();
  };

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="block max-w-md w-full mx-4 gap-0 bg-surface dark:bg-zinc-900 rounded-xl p-6 shadow-2xl"
      >
        <DialogTitle className="text-lg font-semibold mb-2">Temporary Password</DialogTitle>
        <p className="text-sm text-ink-2 mb-4">
          Share this password with the user. It can only be viewed once.
        </p>
        <div className="bg-surface-sunk dark:bg-zinc-800 rounded-lg p-4 text-center font-mono text-lg tracking-wider select-all">
          {tempPassword}
        </div>
        <p className="text-xs text-warning-strong mt-3">
          The user will be prompted to change this password on first login.
        </p>
        <Button
          onClick={onClose}
          className="mt-4 w-full h-auto py-2 bg-ink dark:bg-white text-white dark:text-zinc-900 rounded-lg text-sm font-medium hover:opacity-90"
        >
          Done
        </Button>
      </DialogContent>
    </Dialog>
  );
};

export { TempPasswordDialog };
export type { TempPasswordDialogProps };
