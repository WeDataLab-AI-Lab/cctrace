'use client';

import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { cn } from '@/lib/utils';
import { excludeAccount } from '@/lib/api';
import { canSubmitExclusion } from '@/lib/excluded-account';

interface ExcludeAccountDialogProps {
  onSuccess: () => void;
  onCancel: () => void;
}

// English confirmation phrase (the rest of the dialog is English; a Korean phrase
// would block a non-Korean-reading admin from confirming) — mirrors retention-edit-dialog.
const REQUIRED = 'EXCLUDE';

const ExcludeAccountDialog = ({ onSuccess, onCancel }: ExcludeAccountDialogProps) => {
  const [loginEmail, setLoginEmail] = useState('');
  const [reason, setReason] = useState('');
  const [typed, setTyped] = useState('');

  const mutation = useMutation({
    mutationFn: () => excludeAccount(loginEmail, reason),
  });

  const handleOpenChange = (open: boolean) => {
    if (!open) onCancel();
  };
  // There is no impact-preview API for exclusion (unlike retention), so the
  // typed confirmation is required unconditionally rather than gated on a
  // dangerous/fresh preview check.
  const canSubmit = canSubmitExclusion({
    loginEmail,
    reason,
    pending: mutation.isPending,
    confirmedTyped: typed,
    requiredConfirmation: REQUIRED,
  });

  if (mutation.isSuccess) {
    return (
      <Dialog open onOpenChange={handleOpenChange}>
        <DialogOverlay className="bg-black/30" />
        <DialogContent
          showCloseButton={false}
          className="block w-[420px] max-w-[420px] gap-0 space-y-4 rounded-xl border border-border bg-surface p-6 shadow-2xl"
        >
          <header>
            <DialogTitle className="text-[15px] font-semibold text-ink">Account excluded</DialogTitle>
          </header>
          <p className="text-[13px] text-ink-2">
            <span className="font-mono text-ink">{loginEmail}</span> is now hidden from the dashboard.
          </p>
          <p className="text-[12px] text-ink-3">
            {mutation.data.hidden_events.toLocaleString()} historical events are now hidden.
          </p>
          <Button
            onClick={onSuccess}
            className="w-full h-auto py-2 text-sm font-semibold rounded-lg bg-brand text-white hover:bg-brand-hover"
          >
            Done
          </Button>
        </DialogContent>
      </Dialog>
    );
  }

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[420px] max-w-[420px] gap-0 space-y-4 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <header>
          <DialogTitle className="text-[15px] font-semibold text-ink">Exclude account</DialogTitle>
          <p className="mt-1 text-xs text-ink-2">
            Hides this login_email from every dashboard view. Rows are not deleted — this only reverses.
          </p>
        </header>

        <div className="space-y-1.5">
          <Label className="text-[11px] text-ink-2">Login email</Label>
          <Input
            autoFocus
            type="email"
            value={loginEmail}
            onChange={(e) => setLoginEmail(e.target.value)}
            placeholder="user@example.com"
            className="h-auto rounded-lg border-border px-3 py-2 text-sm shadow-none"
          />
        </div>

        <div className="space-y-1.5">
          <Label className="text-[11px] text-ink-2">Reason</Label>
          <Input
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="e.g. test account, contractor offboarded"
            className="h-auto rounded-lg border-border px-3 py-2 text-sm shadow-none"
          />
        </div>

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

        {mutation.isError && (
          <p className="text-[12px] text-danger">{mutation.error.message}</p>
        )}

        <div className="flex gap-2">
          <Button
            onClick={() => mutation.mutate()}
            disabled={!canSubmit}
            className={cn(
              'flex-1 h-auto py-2 text-sm font-semibold rounded-lg text-white disabled:opacity-30 disabled:cursor-not-allowed',
              'bg-danger hover:bg-danger/90',
            )}
          >
            Exclude
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

export { ExcludeAccountDialog };
export type { ExcludeAccountDialogProps };
