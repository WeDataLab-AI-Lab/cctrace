'use client';

import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { cn } from '@/lib/utils';
import { excludeBillingAccount } from '@/lib/api';
import { canSubmitBillingExclusion } from '@/lib/excluded-billing-account';

interface ExcludeBillingAccountDialogProps {
  onSuccess: () => void;
  onCancel: () => void;
}

// Mirrors ExcludeAccountDialog's confirmation phrase; English for the same
// reason, so a non-Korean-reading admin can confirm.
const REQUIRED = 'EXCLUDE';

// The providers an account can be billed under. A free-text field would invite
// a typo that silently excludes nothing, since the key has to match a
// quota_samples row exactly.
const PROVIDERS = [
  { id: 'openai', label: 'Codex (OpenAI)' },
  { id: 'anthropic', label: 'Claude (Anthropic)' },
] as const;

const ExcludeBillingAccountDialog = ({ onSuccess, onCancel }: ExcludeBillingAccountDialogProps) => {
  const [provider, setProvider] = useState<string>(PROVIDERS[0].id);
  const [accountID, setAccountID] = useState('');
  const [reason, setReason] = useState('');
  const [typed, setTyped] = useState('');

  const mutation = useMutation({
    mutationFn: () => excludeBillingAccount(provider, accountID, reason),
  });

  const handleOpenChange = (open: boolean) => {
    if (!open) onCancel();
  };
  const canSubmit = canSubmitBillingExclusion({
    provider,
    accountID,
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
            <DialogTitle className="text-[15px] font-semibold text-ink">Billing account excluded</DialogTitle>
          </header>
          <p className="text-[13px] text-ink-2">
            <span className="font-mono text-ink">{accountID}</span> is now hidden from every screen.
          </p>
          <p className="text-[12px] text-ink-3">
            {mutation.data.hidden_samples.toLocaleString()} historical quota readings and{' '}
            {mutation.data.hidden_events.toLocaleString()} usage events are now hidden.
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
          <DialogTitle className="text-[15px] font-semibold text-ink">Exclude billing account</DialogTitle>
          <p className="mt-1 text-xs text-ink-2">
            For accounts with no login email to exclude by. Hides the account from usage charts. Rows are
            not deleted, so this reverses.
          </p>
        </header>

        <div className="space-y-1.5">
          <Label className="text-[11px] text-ink-2">Provider</Label>
          <div className="flex gap-1.5">
            {PROVIDERS.map((p) => (
              <button
                key={p.id}
                type="button"
                onClick={() => setProvider(p.id)}
                aria-pressed={provider === p.id}
                className={cn(
                  'flex-1 rounded-lg border px-3 py-2 text-[12px] transition-colors',
                  provider === p.id
                    ? 'border-brand text-brand font-medium'
                    : 'border-border text-ink-3 hover:text-ink',
                )}
              >
                {p.label}
              </button>
            ))}
          </div>
        </div>

        <div className="space-y-1.5">
          <Label className="text-[11px] text-ink-2">Account ID</Label>
          <Input
            autoFocus
            value={accountID}
            onChange={(e) => setAccountID(e.target.value)}
            placeholder="the id shown on the usage chart"
            className="h-auto rounded-lg border-border px-3 py-2 text-sm font-mono shadow-none"
          />
          {/* Case matters: the id is an opaque provider token, so the server
              stores it exactly as typed rather than folding it. */}
          <p className="text-[11px] text-ink-3">Matched exactly, including case.</p>
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

export { ExcludeBillingAccountDialog };
export type { ExcludeBillingAccountDialogProps };
