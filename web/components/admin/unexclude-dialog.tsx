'use client';

import { useMutation } from '@tanstack/react-query';
import { removeExcludedAccount } from '@/lib/api';
import { fmtCost } from '@/components/sessions/session-utils';
import type { ExcludedAccount } from '@/lib/types';
import { UnexcludeConfirmationDialog } from './unexclude-confirmation-dialog';

interface UnexcludeDialogProps {
  account: ExcludedAccount;
  onSuccess: () => void;
  onCancel: () => void;
}

// Unlike ExcludeAccountDialog, un-excluding is reversible (re-excluding takes
// one click), so a plain confirmation dialog is enough — no typed phrase.
const UnexcludeDialog = ({ account, onSuccess, onCancel }: UnexcludeDialogProps) => {
  const mutation = useMutation({
    mutationFn: () => removeExcludedAccount(account.login_email),
    onSuccess,
  });

  const handleConfirm = () => mutation.mutate();

  return (
    <UnexcludeConfirmationDialog
      description={
        <>
          <span className="font-mono text-ink">{account.login_email}</span> will become visible on every dashboard
          again, including <span className="font-medium text-ink">{account.event_count.toLocaleString()} events</span>{' '}
          (<span className="font-medium text-ink">{fmtCost(account.cost_usd)}</span>) it currently hides.
        </>
      }
      errorMessage={mutation.error?.message}
      isPending={mutation.isPending}
      onConfirm={handleConfirm}
      onCancel={onCancel}
    />
  );
};

export { UnexcludeDialog };
export type { UnexcludeDialogProps };
