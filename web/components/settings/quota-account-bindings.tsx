'use client';

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { approveQuotaAccountBinding, fetchAdminQuotaBindings, fetchMyQuotaBindings, requestQuotaAccountBinding, revokeQuotaAccountBinding } from '@/lib/api';
import type { QuotaBillingProvider } from '@/lib/quota-owner-types';
import { useAuth } from '@/components/common/auth-context';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

const providerName = (provider: QuotaBillingProvider): string => provider === 'anthropic' ? 'Claude' : 'Codex';

const QuotaAccountBindings = () => {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const [provider, setProvider] = useState<QuotaBillingProvider>('anthropic');
  const [accountID, setAccountID] = useState('');
  const [references, setReferences] = useState<Record<number, string>>({});
  const [verified, setVerified] = useState<Record<number, boolean>>({});
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState<number | 'request' | null>(null);
  const bindings = useQuery({ queryKey: ['my-quota-bindings'], queryFn: fetchMyQuotaBindings, retry: false });
  const admin = useQuery({
    queryKey: ['admin-quota-bindings', 'pending'],
    queryFn: () => fetchAdminQuotaBindings('pending'),
    enabled: user?.role === 'admin',
    retry: false,
  });
  const approved = useQuery({
    queryKey: ['admin-quota-bindings', 'approved'],
    queryFn: () => fetchAdminQuotaBindings('approved'),
    enabled: user?.role === 'admin',
    retry: false,
  });

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['my-quota-bindings'] }),
      queryClient.invalidateQueries({ queryKey: ['admin-quota-bindings'] }),
      queryClient.invalidateQueries({ queryKey: ['my-quota-usage'] }),
    ]);
  };
  const request = async (event: React.FormEvent) => {
    event.preventDefault();
    setMessage('');
    setBusy('request');
    try {
      await requestQuotaAccountBinding({ billing_provider: provider, account_id: accountID.trim() });
      setAccountID('');
      setMessage('확인 요청을 등록했습니다. 관리자 검증이 끝날 때까지 측정되지 않습니다.');
      await refresh();
    } catch {
      setMessage('계정 요청을 등록하지 못했습니다. 계정 ID와 로그인 상태를 확인하세요.');
    } finally {
      setBusy(null);
    }
  };

  const approve = async (id: number) => {
    setMessage('');
    setBusy(id);
    try {
      await approveQuotaAccountBinding(id, references[id]?.trim() ?? '');
      setMessage('provider console에서 확인한 요청을 승인했습니다.');
      await refresh();
    } catch {
      setMessage('요청을 승인하지 못했습니다. 확인 참조값과 관리자 권한을 확인하세요.');
    } finally {
      setBusy(null);
    }
  };

  const revoke = async (id: number) => {
    setMessage('');
    setBusy(id);
    try {
      await revokeQuotaAccountBinding(id);
      setMessage('계정 승인을 회수했습니다. 새 측정값을 읽거나 받을 수 없습니다.');
      await refresh();
    } catch {
      setMessage('계정 승인을 회수하지 못했습니다.');
    } finally {
      setBusy(null);
    }
  };

  return (
    <section className="min-w-0 space-y-5 rounded-lg border border-border bg-surface p-4 sm:p-6" aria-labelledby="quota-binding-title">
      <div>
        <h2 id="quota-binding-title" className="text-base font-semibold text-ink">개인 사용량 계정 확인</h2>
        <p className="mt-1 text-xs text-ink-3">계정 ID를 등록하면 관리자가 provider console에서 독립 확인한 뒤 사용량 연결을 승인합니다. 승인 전에는 미측정으로 표시됩니다.</p>
        <p className="mt-1 text-xs text-ink-3">사용량은 cctrace 클라이언트 보고값이며 provider가 인증한 측정값이 아닙니다. 이메일·비밀번호·토큰·스크린샷은 입력하거나 보내지 마세요.</p>
      </div>

      <form onSubmit={(event) => { void request(event); }} className="grid min-w-0 gap-3 sm:grid-cols-[180px_minmax(0,1fr)_auto] sm:items-end">
        <div className="space-y-1">
          <Label htmlFor="quota-provider" className="text-xs text-ink-2">Provider</Label>
          <select id="quota-provider" value={provider} onChange={(event) => setProvider(event.target.value as QuotaBillingProvider)} className="h-9 w-full rounded-md border border-border bg-surface px-2 text-sm text-ink">
            <option value="anthropic">Claude</option>
            <option value="openai">Codex</option>
          </select>
        </div>
        <div className="space-y-1">
          <Label htmlFor="quota-account-id" className="text-xs text-ink-2">계정 ID</Label>
          <Input id="quota-account-id" value={accountID} onChange={(event) => setAccountID(event.target.value)} maxLength={256} autoComplete="off" required />
        </div>
        <Button type="submit" disabled={busy !== null || accountID.trim().length === 0}>확인 요청</Button>
      </form>

      {bindings.isError && <p role="status" className="text-xs text-danger">개인 계정 요청을 불러오지 못했습니다.</p>}
      {bindings.data && bindings.data.length > 0 && (
        <div className="space-y-2">
          <h3 className="text-xs font-semibold text-ink">내 요청</h3>
          {bindings.data.map((binding) => (
            <div key={binding.id} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border px-3 py-2 text-xs">
              <span className="min-w-0 break-all text-ink">{providerName(binding.billing_provider)} · …{binding.account_id.slice(-8)}</span>
              <span className="text-ink-3">{binding.status === 'approved' ? '승인됨' : binding.status === 'revoked' ? '회수됨' : '확인 대기'}</span>
            </div>
          ))}
        </div>
      )}

      {user?.role === 'admin' && (
        <div className="space-y-3 border-t border-border pt-4">
          <div>
            <h3 className="text-xs font-semibold text-ink">관리자 확인 대기</h3>
            <p className="mt-1 text-[11px] text-ink-3">요청자가 제공한 화면이나 파일에 의존하지 말고 provider console에서 ID를 독립적으로 확인하세요. 참조값에는 내부 티켓 번호처럼 비밀이 아닌 식별자만 기록하세요.</p>
          </div>
          {admin.isError && <p role="status" className="text-xs text-danger">확인 대기 목록을 불러오지 못했습니다.</p>}
          {admin.data?.map((binding) => (
            <div key={binding.id} className="space-y-3 rounded-md border border-border p-3">
              <p className="break-all text-xs text-ink">{binding.owner_email} · {providerName(binding.billing_provider)} · …{binding.account_id.slice(-8)}</p>
              <div className="grid gap-3 sm:grid-cols-[1fr_auto] sm:items-end">
                <div className="space-y-1">
                  <Label htmlFor={`verify-reference-${binding.id}`} className="text-xs text-ink-2">확인 참조값</Label>
                  <Input id={`verify-reference-${binding.id}`} value={references[binding.id] ?? ''} onChange={(event) => setReferences((current) => ({ ...current, [binding.id]: event.target.value }))} maxLength={128} autoComplete="off" />
                </div>
                <label className="flex items-center gap-2 text-xs text-ink-2">
                  <input type="checkbox" checked={verified[binding.id] ?? false} onChange={(event) => setVerified((current) => ({ ...current, [binding.id]: event.target.checked }))} />
                  provider console에서 독립 확인함
                </label>
              </div>
              <Button type="button" size="sm" disabled={busy !== null || !verified[binding.id] || !references[binding.id]?.trim()} onClick={() => { void approve(binding.id); }}>확인 승인</Button>
            </div>
          ))}
          {admin.data?.length === 0 && !admin.isPending && <p className="text-xs text-ink-3">대기 중인 요청이 없습니다.</p>}
          <div className="space-y-2 border-t border-border pt-3">
            <h3 className="text-xs font-semibold text-ink">승인된 개인 계정</h3>
            {approved.isError && <p role="status" className="text-xs text-danger">승인 계정 목록을 불러오지 못했습니다.</p>}
            {approved.data?.map((binding) => (
              <div key={binding.id} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border px-3 py-2 text-xs">
                <span className="min-w-0 break-all text-ink">{binding.owner_email} · {providerName(binding.billing_provider)} · …{binding.account_id.slice(-8)}</span>
                <Button type="button" size="sm" variant="outline" disabled={busy !== null} onClick={() => { void revoke(binding.id); }}>승인 회수</Button>
              </div>
            ))}
          </div>
        </div>
      )}

      {message && <p role="status" className="text-xs text-ink-2">{message}</p>}
    </section>
  );
};

export { QuotaAccountBindings };
