'use client';

import { useEffect, useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { cn } from '@/lib/utils';
import { fetchRetentionPreview, setRetention } from '@/lib/api';
import {
  canApplyRetention,
  previewIsFresh,
  retentionChangeIsDangerous,
  totalRowsToDrop,
} from '@/lib/retention-edit';
import type { RetentionAxis } from '@/lib/types';

interface RetentionEditDialogProps {
  axis: RetentionAxis;
  axisLabel: string;
  currentDays: number | null;
  onSuccess: () => void;
  onCancel: () => void;
}

// English confirmation phrase (the rest of the dialog is English; a Korean phrase
// would block a non-Korean-reading admin from confirming).
const REQUIRED = 'DELETE';

const RetentionEditDialog = ({ axis, axisLabel, currentDays, onSuccess, onCancel }: RetentionEditDialogProps) => {
  const [daysStr, setDaysStr] = useState(currentDays == null ? '0' : String(currentDays));
  const [typed, setTyped] = useState('');
  const [debouncedDays, setDebouncedDays] = useState<number | null>(null);

  const days = Number.parseInt(daysStr, 10);
  const valid = Number.isInteger(days) && days >= 0;

  // Debounce preview fetches while the operator types. The state update lives
  // inside the timeout (async) so it never runs synchronously in the effect body.
  useEffect(() => {
    const id = setTimeout(() => setDebouncedDays(valid ? days : null), 250);
    return () => clearTimeout(id);
  }, [days, valid]);

  const { data: preview = [], isFetching, isError } = useQuery({
    queryKey: ['retention-preview', axis, debouncedDays],
    queryFn: () => fetchRetentionPreview(axis, debouncedDays as number),
    enabled: debouncedDays != null,
    // Drop-eligibility is now()-relative, so a cached preview can go stale (rows
    // cross the cutoff over time). staleTime:0 forces a refetch when the same days
    // value is re-selected — isFetching goes true, so previewReady fails closed
    // until a fresh count returns. gcTime:0 avoids re-serving an aged snapshot.
    staleTime: 0,
    gcTime: 0,
  });

  // The preview is authoritative ONLY when it reflects the CURRENT days value and
  // loaded cleanly. Anything else — typing, debounce lag (days !== debouncedDays),
  // an in-flight fetch, or a failed fetch — leaves the impact UNKNOWN, so we fail
  // CLOSED: Apply stays disabled. This prevents applying a destructive change while
  // the impact still shows a stale "safe" value.
  const previewReady = previewIsFresh({ valid, daysMatch: debouncedDays === days, isFetching, isError });
  const dangerous = previewReady && retentionChangeIsDangerous(preview);
  const totalDrop = totalRowsToDrop(preview);

  const mutation = useMutation({
    mutationFn: () => setRetention({ axis, days, confirmed: dangerous }),
    onSuccess,
  });

  const handleOpenChange = (open: boolean) => {
    if (!open) onCancel();
  };
  const canApply = canApplyRetention({
    previewFresh: previewReady,
    pending: mutation.isPending,
    dangerous,
    confirmedTyped: typed === REQUIRED,
  });

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[420px] max-w-[420px] gap-0 space-y-4 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <header>
          <DialogTitle className="text-[15px] font-semibold text-ink">Edit retention</DialogTitle>
          <p className="mt-1 text-xs text-ink-2">
            Applies to <span className="font-mono text-ink">{axisLabel}</span>. Current:{' '}
            {currentDays == null ? 'Unlimited' : `${currentDays}d`}.
          </p>
        </header>

        <div className="space-y-1.5">
          <Label className="text-[11px] text-ink-2">Retention days (0 = Unlimited / no policy)</Label>
          <Input
            autoFocus
            type="number"
            min={0}
            value={daysStr}
            onChange={(e) => {
              setDaysStr(e.target.value);
              // Any change to days invalidates a prior confirmation: a confirm
              // typed for one impact must never authorize a different, larger one.
              setTyped('');
            }}
            className="h-auto rounded-lg border-border px-3 py-2 text-sm shadow-none"
          />
        </div>

        <div className="text-[12px]">
          {isError ? (
            <div className="bg-danger-soft border border-danger/30 rounded-lg px-3 py-2">
              <p className="text-[11px] font-semibold text-danger">
                Could not compute impact. Apply is disabled until the preview loads.
              </p>
            </div>
          ) : !previewReady ? (
            <p className="text-ink-3">Calculating impact…</p>
          ) : dangerous ? (
            <div className="bg-danger-soft border border-danger/30 rounded-lg px-3 py-2 space-y-1">
              <p className="text-[11px] font-semibold text-danger uppercase tracking-wide">
                up to {totalDrop.toLocaleString()} rows will be permanently dropped — cannot be undone
              </p>
              {preview
                .filter((row) => row.rows_to_drop > 0)
                .map((row) => (
                  <p key={row.table} className="text-[11px] text-danger-strong">
                    {row.table}: up to {row.rows_to_drop.toLocaleString()} rows
                  </p>
                ))}
            </div>
          ) : (
            <p className="text-ink-3">No rows will be dropped (safe change).</p>
          )}
        </div>

        {dangerous && (
          <div className="space-y-1.5">
            <Label className="text-[11px] text-ink-2">
              Type <span className="font-mono font-semibold text-ink">{REQUIRED}</span> to confirm
            </Label>
            <Input
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder={REQUIRED}
              className="h-auto rounded-lg border-border px-3 py-2 text-sm font-mono shadow-none focus-visible:border-danger"
            />
          </div>
        )}

        {mutation.isError && (
          <p className="text-[12px] text-danger">{mutation.error.message}</p>
        )}

        <div className="flex gap-2">
          <Button
            onClick={() => mutation.mutate()}
            disabled={!canApply}
            className={cn(
              'flex-1 h-auto py-2 text-sm font-semibold rounded-lg text-white disabled:opacity-30 disabled:cursor-not-allowed',
              dangerous ? 'bg-danger hover:bg-danger/90' : 'bg-brand hover:bg-brand-hover',
            )}
          >
            Apply
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

export { RetentionEditDialog };
export type { RetentionEditDialogProps };
