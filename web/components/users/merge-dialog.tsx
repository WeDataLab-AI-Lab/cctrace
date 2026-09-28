'use client';

import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import type { CostSummary } from '@/lib/types';
import { resolveDisplayName } from './user-utils';

interface MergeDialogProps {
  from: CostSummary;
  to: CostSummary;
  onConfirm: () => void;
  onCancel: () => void;
}

const REQUIRED = '확인했습니다';

const MergeDialog = ({ from, to, onConfirm, onCancel }: MergeDialogProps) => {
  const [typed, setTyped] = useState('');
  const confirmed = typed === REQUIRED;
  const fromLabel = resolveDisplayName(from).label;
  const toLabel = resolveDisplayName(to).label;

  const handleOpenChange = (open: boolean) => {
    if (!open) onCancel();
  };
  const handleTypedChange = (e: React.ChangeEvent<HTMLInputElement>) => setTyped(e.target.value);

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[380px] max-w-[380px] gap-0 space-y-4 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <header>
          <DialogTitle className="text-[15px] font-semibold text-ink">병합하시겠습니까?</DialogTitle>
          <p className="mt-1 text-xs text-ink-2 break-all">
            <span className="font-medium text-danger">{fromLabel}</span>의 모든 데이터가{' '}
            <span className="font-medium text-ink">{toLabel}</span>로 이동됩니다.
          </p>
        </header>
        <div className="bg-danger-soft border border-danger/30 rounded-lg px-3 py-2">
          <p className="text-[11px] font-semibold text-danger uppercase tracking-wide">이 결정은 번복할 수 없습니다</p>
        </div>
        <div className="space-y-1.5">
          <Label className="text-[11px] text-ink-2">
            아래에 <span className="font-mono font-semibold text-ink">{REQUIRED}</span>를 입력하세요
          </Label>
          <Input
            autoFocus
            value={typed}
            onChange={handleTypedChange}
            placeholder={REQUIRED}
            className="h-auto rounded-lg border-border px-3 py-2 text-sm font-mono shadow-none focus-visible:ring-0 focus-visible:border-danger"
          />
        </div>
        <div className="flex gap-2">
          <Button
            onClick={onConfirm}
            disabled={!confirmed}
            className="flex-1 h-auto py-2 text-sm font-semibold rounded-lg bg-danger text-white disabled:opacity-30 disabled:cursor-not-allowed hover:bg-danger/90"
          >
            병합
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

export { MergeDialog };
export type { MergeDialogProps };
