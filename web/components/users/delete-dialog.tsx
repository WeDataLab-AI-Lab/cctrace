'use client';

import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import type { CostSummary } from '@/lib/types';
import { resolveDisplayName } from './user-utils';

interface DeleteDialogProps {
  user: CostSummary;
  onConfirm: () => void;
  onCancel: () => void;
}

const DeleteDialog = ({ user, onCancel, onConfirm }: DeleteDialogProps) => {
  const label = resolveDisplayName(user).label;

  const handleOpenChange = (open: boolean) => {
    if (!open) onCancel();
  };

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[340px] max-w-[340px] gap-0 space-y-4 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <header>
          <DialogTitle className="text-[15px] font-semibold text-ink">데이터 삭제</DialogTitle>
          <p className="mt-1 text-xs text-ink-2">
            <span className="font-medium text-ink">{label}</span>의 모든 기록이 삭제됩니다.
          </p>
        </header>
        <div className="bg-danger-soft border border-danger/30 rounded-lg px-3 py-2">
          <p className="text-[11px] font-semibold text-danger uppercase tracking-wide">이 결정은 번복할 수 없습니다</p>
        </div>
        <div className="flex gap-2">
          <Button
            onClick={onConfirm}
            className="flex-1 h-auto py-2 text-sm font-semibold rounded-lg bg-danger text-white hover:bg-danger/90"
          >
            확인
          </Button>
          <Button
            variant="outline"
            onClick={onCancel}
            className="flex-1 h-auto py-2 text-sm font-medium rounded-lg border-border text-ink-2 shadow-none hover:bg-canvas"
          >
            취소
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
};

export { DeleteDialog };
export type { DeleteDialogProps };
