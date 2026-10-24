'use client';

import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog';
import { useAuth } from '@/components/common/auth-context';
import {
  deleteOwnSessionRecords,
  fetchSessionRecordConsent,
  grantSessionRecordConsent,
  SessionRecordRequestError,
  withdrawSessionRecordConsent,
} from '@/lib/api';

type RecordControlDialog = 'consent' | 'withdraw' | 'delete' | null;

const invalidateRecordViews = (invalidate: (filters: { queryKey: readonly unknown[] }) => Promise<unknown>) => {
  for (const key of ['session-record-consent', 'session-analysis', 'session-record-status', 'session-overview', 'session-overview-count', 'session-calendar', 'session-search', 'admin-ai']) {
    void invalidate({ queryKey: [key] });
  }
};

const errorText = (error: unknown): string => {
  if (!(error instanceof SessionRecordRequestError)) return '요청을 처리하지 못했습니다. 다시 시도해 주세요.';
  if (error.code === 'consent_mismatch') return '동의 문구 또는 런타임이 바뀌었습니다. 최신 고지를 다시 확인해 주세요.';
  if (error.code === 'records_disabled') return '관리자가 세션 기록 생성을 껐습니다.';
  if (error.code === 'consent_required') return '현재 런타임에 대한 동의가 필요합니다.';
  return `서버가 ${error.code} 응답을 보냈습니다.`;
};

const consentReadErrorText = (error: unknown): string => error instanceof SessionRecordRequestError && error.code === 'session_records_disabled'
  ? '세션 기록 기능이 꺼져 있어 현재 동의 상태를 읽을 수 없습니다.'
  : '동의 상태를 읽지 못했습니다.';

interface SessionRecordControlsProps {
  /** Non-owner admins must never receive derived record controls or aggregates. */
  ownerView: boolean;
}

const SessionRecordControls = ({ ownerView }: SessionRecordControlsProps) => {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const [dialog, setDialog] = useState<RecordControlDialog>(null);
  const [acknowledged, setAcknowledged] = useState(false);
  const [deleteNotice, setDeleteNotice] = useState<number | null>(null);
  const consent = useQuery({
    queryKey: ['session-record-consent', user?.id ?? null],
    queryFn: ({ signal }) => fetchSessionRecordConsent(signal),
    enabled: ownerView && !!user,
    staleTime: 0,
    retry: false,
  });
  const invalidate = () => invalidateRecordViews((filters) => queryClient.invalidateQueries(filters));
  const grant = useMutation({
    mutationFn: () => consent.data
      ? grantSessionRecordConsent({ runtime_key: consent.data.runtime_key, disclosure_version: consent.data.disclosure_version })
      : Promise.reject(new Error('동의 고지를 받지 못했습니다.')),
    onSuccess: () => {
      setDialog(null);
      setAcknowledged(false);
      invalidate();
    },
    onError: (error) => {
      if (error instanceof SessionRecordRequestError && error.status === 409) void consent.refetch();
    },
  });
  const withdraw = useMutation({
    mutationFn: withdrawSessionRecordConsent,
    onSuccess: () => {
      setDialog(null);
      invalidate();
    },
  });
  const remove = useMutation({
    mutationFn: deleteOwnSessionRecords,
    onSuccess: (result) => {
      setDialog(null);
      setDeleteNotice(result.deleted);
      invalidate();
    },
  });

  const openConsent = () => {
    setAcknowledged(false);
    setDialog('consent');
  };
  const openWithdraw = () => setDialog('withdraw');
  const openDelete = () => setDialog('delete');
  const closeDialog = () => setDialog(null);
  const handleDialogOpenChange = (open: boolean) => {
    if (!open) closeDialog();
  };
  const handleAcknowledge = () => setAcknowledged((previous) => !previous);
  const handleGrant = () => {
    if (acknowledged && consent.data) grant.mutate();
  };
  const handleWithdraw = () => withdraw.mutate();
  const handleDelete = () => remove.mutate();
  const handleReloadConsent = () => { void consent.refetch(); };

  if (!ownerView || !user) return null;

  return (
    <section aria-label="세션 기록 개인정보 제어" className="rounded-[var(--r-sm)] border border-border bg-surface px-3 py-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-[11.5px] font-medium text-ink">세션 기록 전송 동의 및 내 기록 관리</h3>
          {consent.isPending ? <p role="status" className="text-[10.5px] text-ink-3">동의 상태를 읽는 중입니다.</p>
            : consent.isError ? <p role="alert" className="text-[10.5px] text-danger">{consentReadErrorText(consent.error)} <button type="button" onClick={handleReloadConsent} className="underline">다시 읽기</button></p>
              : <p className="text-[10.5px] text-ink-3">
                {consent.data?.granted ? '현재 런타임에 동의했습니다.' : '현재 런타임에는 동의하지 않았습니다.'}
                {consent.data?.provider ? ` · 공급자 ${consent.data.provider}` : ''}
              </p>}
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-1.5">
          {consent.data && !consent.data.granted && (
            <Button type="button" variant="outline" size="sm" onClick={openConsent} className="h-7 px-2 text-[10.5px]">
              동의 검토
            </Button>
          )}
          {consent.data?.granted && (
            <Button type="button" variant="outline" size="sm" onClick={openWithdraw} className="h-7 px-2 text-[10.5px]">
              동의 철회
            </Button>
          )}
          <Button type="button" variant="outline" size="sm" onClick={openDelete} disabled={remove.isPending} className="h-7 px-2 text-[10.5px] text-danger">
            내 기록 삭제
          </Button>
        </div>
      </div>
      {consent.data && !consent.data.enabled && <p className="mt-1 text-[10.5px] text-warning-strong">관리자가 세션 기록 생성을 껐습니다.</p>}
      {consent.data && !consent.data.runtime_available && (
        <p className="mt-1 text-[10.5px] text-warning-strong">런타임을 사용할 수 없습니다: {consent.data.runtime_reason || '사유를 받지 못함'}</p>
      )}
      {deleteNotice !== null && <p role="status" className="mt-1 text-[10.5px] text-ink-2">내 파생 세션 기록 {deleteNotice}건을 삭제했습니다. 동의 상태는 그대로입니다.</p>}

      <Dialog open={dialog !== null} onOpenChange={handleDialogOpenChange}>
        <DialogContent showCloseButton={false} aria-describedby="session-record-dialog-description" className="block w-[min(560px,calc(100vw-2rem))] max-h-[calc(100dvh-2rem)] max-w-[calc(100vw-2rem)] space-y-4 overflow-y-auto border-border bg-surface">
          {dialog === 'consent' && (
            <>
              <DialogTitle className="text-[15px] font-semibold text-ink">세션 대화 일부를 AI 공급자에게 보냅니다</DialogTitle>
              {consent.isPending && <p role="status" className="text-[12px] text-ink-3">최신 고지를 읽는 중입니다.</p>}
              {consent.data && (
                <div className="space-y-3 text-[12px] leading-relaxed text-ink-2">
                  <p id="session-record-dialog-description">{consent.data.disclosure}</p>
                  <p>런타임: <span className="font-mono text-ink">{consent.data.runtime_key}</span> · 공급자: {consent.data.provider || '미상'} · 보관 기간: {consent.data.provider_retention}</p>
                  <section>
                    <h4 className="font-medium text-ink">보내는 항목</h4>
                    <ul className="list-disc pl-5">{consent.data.sends.map((line) => <li key={line}>{line}</li>)}</ul>
                  </section>
                  <section>
                    <h4 className="font-medium text-ink">보내지 않는 항목</h4>
                    <ul className="list-disc pl-5">{consent.data.not_sends.map((line) => <li key={line}>{line}</li>)}</ul>
                  </section>
                  <label className="flex items-start gap-2 rounded border border-border p-2 text-ink">
                    <input type="checkbox" checked={acknowledged} onChange={handleAcknowledge} className="mt-0.5 accent-[var(--accent)]" />
                    <span>고지 내용을 읽었고 전송에 동의합니다</span>
                  </label>
                </div>
              )}
              {consent.data && (!consent.data.enabled || !consent.data.runtime_available) && (
                <p role="status" className="text-[11px] text-warning-strong">관리자 설정 또는 런타임 상태 때문에 지금은 동의할 수 없습니다.</p>
              )}
              {grant.error && <p role="alert" className="text-[11px] text-danger">{errorText(grant.error)}</p>}
              <footer className="flex justify-end gap-2">
                <Button type="button" variant="outline" size="sm" onClick={closeDialog}>취소</Button>
                <Button type="button" size="sm" onClick={handleGrant} disabled={!consent.data?.enabled || !consent.data.runtime_available || !acknowledged || grant.isPending}>
                  동의하고 저장
                </Button>
              </footer>
            </>
          )}
          {dialog === 'withdraw' && (
            <>
              <DialogTitle className="text-[15px] font-semibold text-ink">세션 기록 전송 동의를 철회할까요?</DialogTitle>
              <p id="session-record-dialog-description" className="text-[12px] leading-relaxed text-ink-2">철회 뒤에는 새 외부 전송을 시작하지 않습니다. 공급자가 이미 받은 원문은 회수할 수 없고, 서버에 저장된 파생 기록은 남습니다. 파생 기록 삭제는 별도 작업입니다.</p>
              {withdraw.error && <p role="alert" className="text-[11px] text-danger">{errorText(withdraw.error)}</p>}
              <footer className="flex justify-end gap-2">
                <Button type="button" variant="outline" size="sm" onClick={closeDialog}>취소</Button>
                <Button type="button" size="sm" onClick={handleWithdraw} disabled={withdraw.isPending}>동의 철회 확인</Button>
              </footer>
            </>
          )}
          {dialog === 'delete' && (
            <>
              <DialogTitle className="text-[15px] font-semibold text-ink">내 파생 세션 기록을 삭제할까요?</DialogTitle>
              <p id="session-record-dialog-description" className="text-[12px] leading-relaxed text-ink-2">내 세션에서 생성한 개요·검토 사항·작업 항목·태그를 삭제합니다. 동의 철회와 별개로 전송 동의는 유지됩니다. 대화 원문과 공급자가 이미 받은 내용은 이 작업으로 삭제되지 않습니다.</p>
              {remove.error && <p role="alert" className="text-[11px] text-danger">{errorText(remove.error)}</p>}
              <footer className="flex justify-end gap-2">
                <Button type="button" variant="outline" size="sm" onClick={closeDialog}>취소</Button>
                <Button type="button" variant="destructive" size="sm" onClick={handleDelete} disabled={remove.isPending}>내 기록 삭제 확인</Button>
              </footer>
            </>
          )}
        </DialogContent>
      </Dialog>
    </section>
  );
};

export { SessionRecordControls };
