'use client';

import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { deleteProject, type DeleteSessionResult } from '@/lib/api';

interface DeleteProjectDialogProps {
  /** Every real project_hash the chosen row stands for. */
  projectHashes: string[];
  projectName: string;
  onDeleted: (result: DeleteSessionResult) => void;
  onCancel: () => void;
}

/**
 * Deleting a project from the picker.
 *
 * Two steps, not the session dialog's three: there is no "and the others?" to ask
 * when the project itself is what was chosen. What remains is the delete and the
 * standing refusal, which are separate decisions -- one clears what is stored, the
 * other changes what the server accepts from then on.
 */
type Step = 'confirm-delete' | 'ask-block';

const DeleteProjectDialog = ({
  projectHashes,
  projectName,
  onDeleted,
  onCancel,
}: DeleteProjectDialogProps) => {
  const [step, setStep] = useState<Step>('confirm-delete');

  const mutation = useMutation({
    mutationFn: (blockProject: boolean) => deleteProject(projectHashes, { blockProject }),
    onSuccess: (data) => onDeleted(data),
  });

  const shell = (title: string, body: React.ReactNode, footer: React.ReactNode) => (
    <Dialog open onOpenChange={(open) => !open && onCancel()}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[420px] max-w-[420px] gap-0 space-y-3.5 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <header>
          <DialogTitle className="text-[14px] font-semibold text-ink">{title}</DialogTitle>
        </header>
        {body}
        <div className="flex justify-end gap-2 pt-0.5">{footer}</div>
      </DialogContent>
    </Dialog>
  );

  const bodyClass =
    'space-y-1.5 text-[12px] leading-relaxed text-ink-2 [overflow-wrap:anywhere] [text-wrap:pretty] break-keep';
  const err = mutation.isError ? (
    <p className="text-[11px] text-danger">{mutation.error.message}</p>
  ) : null;

  if (step === 'ask-block') {
    return shell(
      '이 프로젝트를 계속 수집할까요?',
      <div className={bodyClass}>
        <p>
          <span className="font-mono text-ink">{projectName}</span> 의 세션이 앞으로도 등록되지 않도록
          하시겠습니까?
        </p>
        <p className="text-[11px] leading-relaxed text-ink-3">
          앞으로 들어오는 것만 거부합니다. 설정에서 언제든 해제할 수 있습니다.
        </p>
        {err}
      </div>,
      <>
        <Button
          variant="ghost"
          className="h-[30px] px-3 text-[12px]"
          disabled={mutation.isPending}
          onClick={() => mutation.mutate(false)}
        >
          아니오
        </Button>
        <Button
          variant="destructive"
          className="h-[30px] px-3 text-[12px]"
          disabled={mutation.isPending}
          onClick={() => mutation.mutate(true)}
        >
          예, 차단합니다
        </Button>
      </>,
    );
  }

  return shell(
    '이 프로젝트를 삭제하시겠습니까?',
    <div className={bodyClass}>
      <p>
        <span className="font-mono text-ink">{projectName}</span> 에 저장된 세션과 그에 딸린
        이벤트·메트릭이 서버에서 제거됩니다.
      </p>
      <p className="text-[11px] leading-relaxed text-ink-3">되돌릴 수 없습니다.</p>
      {err}
    </div>,
    <>
      <Button
        variant="ghost"
        className="h-[30px] px-3 text-[12px]"
        disabled={mutation.isPending}
        onClick={onCancel}
      >
        아니오
      </Button>
      <Button
        variant="destructive"
        className="h-[30px] px-3 text-[12px]"
        disabled={mutation.isPending}
        onClick={() => setStep('ask-block')}
      >
        예, 삭제합니다
      </Button>
    </>,
  );
};

export { DeleteProjectDialog };
