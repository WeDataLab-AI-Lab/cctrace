'use client';

import { useMutation, useQuery } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog';
import { AIReportRequestError, fetchAIConsent, grantAIConsent } from '@/lib/api';
import type { AIConsentInfo } from '@/lib/types';

interface ConsentDisclosureProps {
  info: AIConsentInfo;
}

/** Every line comes from the server for the current runtime; the screen adds no
 *  item of its own (spec §4). */
const ConsentDisclosure = ({ info }: ConsentDisclosureProps) => (
  <div className="space-y-3 text-[13px] text-ink-2">
    <p>
      런타임: <span className="font-mono text-ink">{info.runtime_key}</span>
    </p>
    <p>
      공급자: <span className="text-ink">{info.provider}</span>
    </p>
    <section>
      <h3 className="text-[12.5px] font-medium text-ink">보내는 것</h3>
      <ul className="mt-1 list-disc space-y-0.5 pl-5">
        {info.sends.map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>
    </section>
    <section>
      <h3 className="text-[12.5px] font-medium text-ink">보내지 않는 것</h3>
      <ul className="mt-1 list-disc space-y-0.5 pl-5">
        {info.not_sends.map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>
    </section>
    <p>공급자 보관: {info.provider_retention}</p>
  </div>
);

interface ConsentDialogProps {
  userId: number | null;
  onCancel: () => void;
  /** Called after the consent is recorded; the caller starts the analysis. */
  onAgreed: () => void;
}

const ConsentDialog = ({ userId, onCancel, onAgreed }: ConsentDialogProps) => {
  // Dialog-scoped: read when opened, never polled.
  const { data: info, isLoading, isError, refetch } = useQuery({
    queryKey: ['ai-consent', userId],
    queryFn: fetchAIConsent,
    staleTime: 0,
  });
  const grant = useMutation({
    mutationFn: (current: AIConsentInfo) =>
      grantAIConsent({ runtime_key: current.runtime_key, disclosure_version: current.disclosure_version }),
    onSuccess: onAgreed,
    // The disclosure changed while it was open: show the current one again.
    onError: (error) => {
      if (error instanceof AIReportRequestError && error.status === 409) void refetch();
    },
  });

  const outdated = grant.error instanceof AIReportRequestError && grant.error.status === 409;
  const handleOpenChange = (open: boolean) => {
    if (!open) onCancel();
  };
  const handleAgree = () => {
    if (info) grant.mutate(info);
  };

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogContent showCloseButton={false} className="block max-w-[520px] space-y-4 border-border bg-surface">
        <DialogTitle className="text-[15px] font-semibold text-ink">분석 데이터가 외부로 전송됩니다</DialogTitle>
        {isLoading && <p className="text-[13px] text-ink-3">불러오는 중</p>}
        {isError && <p className="text-[13px] text-danger">전송 항목을 불러오지 못했습니다</p>}
        {info && <ConsentDisclosure info={info} />}
        {outdated && <p className="text-[12.5px] text-danger">안내 문구가 바뀌었습니다. 다시 확인한 뒤 동의해 주세요</p>}
        {grant.error && !outdated && <p className="text-[12.5px] text-danger">동의를 기록하지 못했습니다</p>}
        <footer className="flex justify-end gap-2">
          <Button type="button" variant="outline" size="sm" onClick={onCancel}>
            취소
          </Button>
          <Button type="button" size="sm" className="bg-brand text-brand-ink hover:bg-brand-hover" onClick={handleAgree} disabled={!info || grant.isPending}>
            동의하고 분석 시작
          </Button>
        </footer>
      </DialogContent>
    </Dialog>
  );
};

export { ConsentDialog, ConsentDisclosure };
