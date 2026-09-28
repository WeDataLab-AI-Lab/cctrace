'use client';

import { useRef, useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { ADMIN_AI_KEY, ADMIN_AI_MODELS_KEY, ApiKeyForm, OUTLINE_BUTTON_CLASS } from '@/components/admin/ai-account-section';
import { AIReportRequestError, deleteAdminAIProviderKey, setAdminAIProviderKey } from '@/lib/api';
import { credentialErrorMessage, credentialLockReason, credentialSummary } from '@/lib/admin-ai-runtime';
import type { AdminAICredential } from '@/lib/types';

const PROVIDER_LABEL: Record<string, { name: string; placeholder: string }> = {
  openai: { name: 'OpenAI', placeholder: 'sk-...' },
  anthropic: { name: 'Anthropic', placeholder: 'sk-ant-...' },
  nvidia: { name: 'NVIDIA', placeholder: 'nvapi-...' },
  litellm: { name: 'LiteLLM', placeholder: 'sk-...' },
};

const errorText = (error: unknown): string | null => {
  if (!error) return null;
  return credentialErrorMessage(error instanceof AIReportRequestError ? error.code : '');
};

interface DeleteKeyDialogProps {
  name: string;
  pending: boolean;
  errorMessage: string | null;
  onConfirm: () => void;
  onCancel: () => void;
}

const DeleteKeyDialog = ({ name, pending, errorMessage, onConfirm, onCancel }: DeleteKeyDialogProps) => {
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
          <DialogTitle className="text-[15px] font-semibold text-ink">{name} API 키 삭제</DialogTitle>
        </header>
        <p className="text-[13px] text-ink-2">삭제하면 이 키를 쓰는 런타임의 리포트 생성이 멈춥니다. 키를 다시 등록할 때까지 모든 사용자의 리포트 생성이 실패합니다.</p>
        {errorMessage && <p className="text-[12px] text-danger">{errorMessage}</p>}
        <div className="flex gap-2">
          <Button
            onClick={onConfirm}
            disabled={pending}
            className="flex-1 h-auto py-2 text-sm font-semibold rounded-lg bg-danger text-white hover:bg-danger-strong disabled:opacity-30"
          >
            삭제
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

const ProviderKeyRow = ({ credential }: { credential: AdminAICredential }) => {
  const queryClient = useQueryClient();
  const [formOpen, setFormOpen] = useState(false);
  const [saved, setSaved] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  // The key waits here only until the request takes it, so it never becomes
  // mutation variables that React Query keeps after the request.
  const keyRef = useRef<string | null>(null);
  // Kept apart from the mutation, which is reset as soon as it settles.
  const [keyError, setKeyError] = useState<unknown>(null);
  const provider = credential.provider;
  const label = PROVIDER_LABEL[provider] ?? { name: provider, placeholder: '' };

  // Runtime status and the catalog both depend on the key.
  const refresh = () => {
    for (const queryKey of [ADMIN_AI_KEY, ADMIN_AI_MODELS_KEY]) {
      void queryClient.invalidateQueries({ queryKey });
    }
  };

  const registerKey = useMutation({
    mutationFn: () => {
      const key = keyRef.current ?? '';
      keyRef.current = null;
      return setAdminAIProviderKey(provider, key);
    },
    onSuccess: () => {
      setFormOpen(false);
      setSaved(true);
      refresh();
    },
    onError: (error) => setKeyError(error),
  });
  const deleteKey = useMutation({
    mutationFn: () => deleteAdminAIProviderKey(provider),
    onSuccess: () => {
      setDeleteOpen(false);
      setSaved(false);
      refresh();
    },
  });

  // Pressing the button again closes the form, exactly as 닫기 does: while the
  // form is open the button is the one thing still pointing at it, so a second
  // press reads as undo rather than as a second request to open what is open.
  const handleToggleForm = () => {
    if (formOpen) {
      setFormOpen(false);
      return;
    }
    setKeyError(null);
    setSaved(false);
    setFormOpen(true);
  };
  const handleCloseForm = () => setFormOpen(false);
  const handleRegister = (key: string) => {
    keyRef.current = key;
    setKeyError(null);
    // Reset once settled, so nothing of the request stays in the mutation.
    registerKey.mutate(undefined, { onSettled: () => registerKey.reset() });
  };
  const handleOpenDelete = () => {
    deleteKey.reset();
    setDeleteOpen(true);
  };
  const handleCloseDelete = () => setDeleteOpen(false);
  const handleDelete = () => deleteKey.mutate();

  const lockReason = credentialLockReason(credential);
  const locked = lockReason !== null;
  const busy = registerKey.isPending || deleteKey.isPending;

  return (
    <li className="space-y-2">
      <header className="flex items-baseline justify-between gap-3 text-[13px]">
        <span className="text-ink shrink-0">{label.name}</span>
        <span className="text-[12px] text-ink-2 truncate">{credentialSummary(credential)}</span>
      </header>
      {credential.reason && <p className="text-[12px] text-danger">{credential.reason}</p>}

      <div className="flex flex-wrap gap-2">
        <Button variant="outline" onClick={handleToggleForm} disabled={locked || busy} className={OUTLINE_BUTTON_CLASS}>
          {credential.source === 'admin' ? 'API 키 교체' : 'API 키 등록'}
        </Button>
        <Button
          variant="outline"
          onClick={handleOpenDelete}
          disabled={locked || busy || credential.source !== 'admin'}
          className={OUTLINE_BUTTON_CLASS}
        >
          삭제
        </Button>
      </div>
      {lockReason && <p className="text-[11px] text-ink-3">{lockReason}</p>}

      {formOpen && !locked && (
        <ApiKeyForm
          label={`${label.name} API 키`}
          placeholder={label.placeholder}
          pending={registerKey.isPending}
          onSubmit={handleRegister}
          onClose={handleCloseForm}
        />
      )}
      {saved && <p className="text-[12px] text-success">API 키를 등록했습니다.</p>}
      {errorText(keyError) && <p className="text-[12px] text-danger">{errorText(keyError)}</p>}

      {deleteOpen && (
        <DeleteKeyDialog
          name={label.name}
          pending={deleteKey.isPending}
          errorMessage={errorText(deleteKey.error)}
          onConfirm={handleDelete}
          onCancel={handleCloseDelete}
        />
      )}
    </li>
  );
};

interface ProviderKeysSectionProps {
  credentials: AdminAICredential[];
}

const ProviderKeysSection = ({ credentials }: ProviderKeysSectionProps) => (
  <section className="bg-surface border border-border rounded-lg px-4 py-4 space-y-3">
    <header>
      <h3 className="text-[13px] font-medium text-ink">API 키</h3>
      <p className="text-[11px] text-ink-3 mt-0.5">OpenAI API·Claude API 런타임이 쓰는 키입니다. 키는 암호화해 저장하며 끝 4자리만 표시합니다.</p>
    </header>
    <ul className="space-y-4">
      {credentials.map((credential) => (
        <ProviderKeyRow key={credential.provider} credential={credential} />
      ))}
    </ul>
  </section>
);

export { ProviderKeysSection };
