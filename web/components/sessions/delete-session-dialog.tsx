'use client';

import { useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { deleteSession, fetchProjectSessionCount, type DeleteSessionResult } from '@/lib/api';

interface DeleteSessionDialogProps {
  sessionId: string;
  projectHash: string;
  projectName: string;
  /** Only an admin may reach past this session, so steps 2 and 3 are hidden otherwise. */
  canActOnProject: boolean;
  onDeleted: (result: DeleteSessionResult) => void;
  onCancel: () => void;
}

/**
 * Three questions, asked in order, because they are three different decisions.
 *
 *   1. remove this session
 *   2. remove the project's other stored sessions
 *   3. refuse the project's future sessions
 *
 * Steps 2 and 3 look similar and are not: one erases history, the other changes
 * what the server accepts from everyone working in that project. Answering yes to
 * either does not imply the other, and the copy says which is which -- a single
 * "delete everything" checkbox would let a click aimed at one do all three.
 *
 * All answers travel in ONE request. Two or three calls can half-fail, leaving the
 * clicked session gone, its siblings present, and nothing on screen saying so.
 */
type Step = 'confirm-delete' | 'ask-purge' | 'ask-block';

const DeleteSessionDialog = ({
  sessionId,
  projectHash,
  projectName,
  canActOnProject,
  onDeleted,
  onCancel,
}: DeleteSessionDialogProps) => {
  const [step, setStep] = useState<Step>('confirm-delete');
  const [purge, setPurge] = useState(false);

  // Fetched up front so step 2 can name the number. Asking "and the others?"
  // without saying how many makes the user guess what they are about to remove.
  const { data: siblingCount = 0 } = useQuery({
    queryKey: ['project-session-count', projectHash, sessionId],
    queryFn: () => fetchProjectSessionCount(projectHash, sessionId),
    enabled: canActOnProject && !!projectHash,
  });

  const mutation = useMutation({
    mutationFn: (opts: { purgeProject: boolean; blockProject: boolean }) =>
      deleteSession(sessionId, opts),
    // No completion screen. The server has only written a tombstone by the time this
    // fires -- reclaiming the rows takes another 15 to 30 seconds in the background --
    // so a dialog saying "deleted, N rows" would be reporting work that has not
    // happened. The list closing the modal and reloading is the honest signal.
    onSuccess: (data) => onDeleted(data),
  });

  const finish = (blockProject: boolean) => mutation.mutate({ purgeProject: purge, blockProject });

  // Nothing to ask about when there is no project, or no permission to act on one.
  const projectStepsAvailable = canActOnProject && !!projectHash && !!projectName;

  const confirmDelete = () => {
    if (!projectStepsAvailable) {
      mutation.mutate({ purgeProject: false, blockProject: false });
      return;
    }
    // Skip step 2 when this is the project's only session -- "and the other 0?" is
    // not a question.
    setStep(siblingCount > 0 ? 'ask-purge' : 'ask-block');
  };

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

  if (step === 'ask-purge') {
    return shell(
      '이 프로젝트의 다른 세션도 지울까요?',
      <div className={bodyClass}>
        <p>
          <span className="font-mono text-ink">{projectName}</span> 에 저장된 다른 세션{' '}
          <span className="font-medium text-ink">{siblingCount.toLocaleString()}개</span>도 지금 함께
          삭제할까요?
        </p>
        <p className="text-[11px] leading-relaxed text-ink-3">
          되돌릴 수 없습니다. 아니오를 고르면 이 세션만 지워집니다.
        </p>
        {err}
      </div>,
      <>
        <Button
          variant="ghost"
          className="h-[30px] px-3 text-[12px]"
          disabled={mutation.isPending}
          onClick={() => {
            setPurge(false);
            setStep('ask-block');
          }}
        >
          아니오
        </Button>
        <Button
          variant="destructive"
          className="h-[30px] px-3 text-[12px]"
          disabled={mutation.isPending}
          onClick={() => {
            setPurge(true);
            setStep('ask-block');
          }}
        >
          예, 함께 삭제합니다
        </Button>
      </>,
    );
  }

  if (step === 'ask-block') {
    return shell(
      '이 프로젝트를 계속 수집할까요?',
      <div className={bodyClass}>
        <p>
          <span className="font-mono text-ink">{projectName}</span> 의 세션이 앞으로도 등록되지 않도록
          하시겠습니까?
        </p>
        <p className="text-[11px] leading-relaxed text-ink-3">
          {purge
            ? '앞으로 들어오는 것만 거부합니다. 설정에서 언제든 해제할 수 있습니다.'
            : '이미 저장된 다른 세션은 그대로 둡니다. 앞으로 들어오는 것만 거부합니다. 설정에서 언제든 해제할 수 있습니다.'}
        </p>
        {err}
      </div>,
      <>
        <Button
          variant="ghost"
          className="h-[30px] px-3 text-[12px]"
          disabled={mutation.isPending}
          onClick={() => finish(false)}
        >
          아니오
        </Button>
        <Button
          variant="destructive"
          className="h-[30px] px-3 text-[12px]"
          disabled={mutation.isPending}
          onClick={() => finish(true)}
        >
          예, 차단합니다
        </Button>
      </>,
    );
  }

  return shell(
    '이 세션을 삭제하시겠습니까?',
    <div className={bodyClass}>
      <p className="font-mono break-all text-[11px] text-ink-3">{sessionId}</p>
      <p>세션 레코드와 그에 딸린 이벤트·메트릭이 서버에서 제거됩니다. 되돌릴 수 없습니다.</p>
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
        onClick={confirmDelete}
      >
        예, 삭제합니다
      </Button>
    </>,
  );
};

export { DeleteSessionDialog };
