'use client';

import type { ReactNode } from 'react';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';

interface UnexcludeConfirmationDialogProps {
  description: ReactNode;
  errorMessage?: string;
  isPending: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

const UnexcludeConfirmationDialog = ({
  description,
  errorMessage,
  isPending,
  onConfirm,
  onCancel,
}: UnexcludeConfirmationDialogProps) => {
  const handleOpenChange = (open: boolean) => {
    if (!open) onCancel();
  };

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[420px] max-w-[420px] gap-0 space-y-4 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <header>
          <DialogTitle className="text-[15px] font-semibold text-ink">Remove exclusion</DialogTitle>
        </header>

        <p className="text-[13px] text-ink-2">{description}</p>

        {errorMessage && <p className="text-[12px] text-danger">{errorMessage}</p>}

        <div className="flex gap-2">
          <Button
            onClick={onConfirm}
            disabled={isPending}
            className="flex-1 h-auto py-2 text-sm font-semibold rounded-lg bg-brand text-white hover:bg-brand-hover disabled:opacity-30"
          >
            Remove exclusion
          </Button>
          <Button
            variant="outline"
            onClick={onCancel}
            className="flex-1 h-auto py-2 text-sm font-medium rounded-lg border-border text-ink-2 shadow-none hover:bg-canvas"
          >
            Cancel
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
};

export { UnexcludeConfirmationDialog };
