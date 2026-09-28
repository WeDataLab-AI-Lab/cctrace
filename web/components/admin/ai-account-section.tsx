'use client';

import { useEffect, useRef, useState } from 'react';
import type { ChangeEvent, FormEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import {
  AIReportRequestError,
  cancelAdminAILogin,
  fetchAdminAIAccount,
  fetchAdminAILogin,
  logoutAdminAI,
  startAdminAILogin,
} from '@/lib/api';
import {
  LOGIN_RESULT,
  accountErrorMessage,
  accountLockReason,
  loginErrorMessage,
  remainingLabel,
  verificationHref,
} from '@/lib/admin-ai-account';
import { POLL_LIVE } from '@/lib/query-config';
import { cn } from '@/lib/utils';
import type { AdminAIAccount, AdminAIDeviceLogin } from '@/lib/types';

const ADMIN_AI_KEY = ['admin-ai'];
const ADMIN_AI_MODELS_KEY = ['admin-ai-models'];
const ADMIN_AI_ACCOUNT_KEY = ['admin-ai-account'];
const loginKey = (loginId: string | null) => ['admin-ai-login', loginId];

const OUTLINE_BUTTON_CLASS =
  'h-auto py-1.5 px-3 text-[12px] font-medium rounded-lg border-border text-ink-2 shadow-none hover:bg-canvas disabled:opacity-40';
const PRIMARY_BUTTON_CLASS =
  'h-auto py-1.5 px-3 text-[12px] font-semibold rounded-lg text-white bg-brand hover:bg-brand-hover disabled:opacity-30 disabled:cursor-not-allowed';

const errorText = (error: unknown): string | null => {
  if (!error) return null;
  return accountErrorMessage(error instanceof AIReportRequestError ? error.code : '');
};

interface DeviceLoginPanelProps {
  login: AdminAIDeviceLogin;
  checkedAt: number;
  canceling: boolean;
  onCancel: () => void;
}

const DeviceLoginPanel = ({ login, checkedAt, canceling, onCancel }: DeviceLoginPanelProps) => {
  const [copied, setCopied] = useState(false);
  const handleCopy = () => {
    void navigator.clipboard.writeText(login.user_code).then(() => setCopied(true));
  };
  const href = verificationHref(login.verification_url);

  return (
    <div className="rounded-lg border border-border bg-canvas px-3 py-3 space-y-2">
      <p className="text-[12px] text-ink-2">
        아래 주소를 열어 ChatGPT 계정으로 로그인한 뒤 코드를 입력하세요. 입력하면 자동으로 연결됩니다.
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <code className="rounded-md bg-surface border border-border px-2 py-1 text-[15px] font-semibold tracking-widest text-ink tabular-nums">
          {login.user_code}
        </code>
        <Button variant="outline" onClick={handleCopy} className={OUTLINE_BUTTON_CLASS}>
          {copied ? '복사됨' : '코드 복사'}
        </Button>
      </div>
      <p className="text-[12px]">
        {href ? (
          <a href={href} target="_blank" rel="noopener noreferrer" className="text-brand underline break-all">
            {href}
          </a>
        ) : (
          <span className="text-ink-2 break-all">{login.verification_url}</span>
        )}
      </p>
      <footer className="flex items-center justify-between gap-2">
        <span className="text-[11px] text-ink-3 tabular-nums">{remainingLabel(login.expires_at, checkedAt)}</span>
        <Button variant="outline" onClick={onCancel} disabled={canceling} className={OUTLINE_BUTTON_CLASS}>
          로그인 취소
        </Button>
      </footer>
    </div>
  );
};

interface ApiKeyFormProps {
  label: string;
  placeholder: string;
  pending: boolean;
  onSubmit: (key: string) => void;
  onClose: () => void;
}

const ApiKeyForm = ({ label, placeholder, pending, onSubmit, onClose }: ApiKeyFormProps) => {
  const [value, setValue] = useState('');
  const handleChange = (event: ChangeEvent<HTMLInputElement>) => setValue(event.target.value);
  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const key = value.trim();
    // Cleared before the request goes out, so the key does not stay in the form.
    setValue('');
    if (key) onSubmit(key);
  };

  return (
    <form onSubmit={handleSubmit} className="flex flex-wrap items-center gap-2">
      <input
        type="password"
        // new-password keeps browsers and password managers from offering to save the key.
        autoComplete="new-password"
        data-1p-ignore
        data-lpignore="true"
        spellCheck={false}
        aria-label={label}
        placeholder={placeholder}
        value={value}
        onChange={handleChange}
        className="min-w-0 flex-1 rounded-lg border border-border bg-surface px-3 py-1.5 text-[13px] text-ink outline-none focus-visible:border-brand"
      />
      <Button type="submit" disabled={pending || !value.trim()} className={PRIMARY_BUTTON_CLASS}>
        등록
      </Button>
      <Button type="button" variant="outline" onClick={onClose} className={OUTLINE_BUTTON_CLASS}>
        닫기
      </Button>
    </form>
  );
};

interface LogoutDialogProps {
  pending: boolean;
  errorMessage: string | null;
  onConfirm: () => void;
  onCancel: () => void;
}

const LogoutDialog = ({ pending, errorMessage, onConfirm, onCancel }: LogoutDialogProps) => {
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
          <DialogTitle className="text-[15px] font-semibold text-ink">연결 계정 로그아웃</DialogTitle>
        </header>
        <p className="text-[13px] text-ink-2">로그아웃하면 AI 리포트 생성이 멈춥니다. 다시 로그인하거나 API 키를 등록할 때까지 모든 사용자의 리포트 생성이 실패합니다.</p>
        {errorMessage && <p className="text-[12px] text-danger">{errorMessage}</p>}
        <div className="flex gap-2">
          <Button
            onClick={onConfirm}
            disabled={pending}
            className="flex-1 h-auto py-2 text-sm font-semibold rounded-lg bg-danger text-white hover:bg-danger-strong disabled:opacity-30"
          >
            로그아웃
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

interface AccountSectionProps {
  authLabel: Record<string, string>;
}

const AccountSection = ({ authLabel }: AccountSectionProps) => {
  const queryClient = useQueryClient();
  // The login started from this tab; a login already pending on the server is
  // picked up from the account instead.
  const [startedLoginId, setStartedLoginId] = useState<string | null>(null);
  const [keyFormOpen, setKeyFormOpen] = useState(false);
  const [keySaved, setKeySaved] = useState(false);
  const [logoutOpen, setLogoutOpen] = useState(false);
  // The key waits here only until the request takes it, so it never becomes
  // mutation variables that React Query keeps after the request.
  const keyRef = useRef<string | null>(null);
  // Kept apart from the mutation, which is reset as soon as it settles.
  const [keyError, setKeyError] = useState<unknown>(null);

  // No refetchInterval: the account changes only through this section, and
  // each change refreshes it.
  const account = useQuery<AdminAIAccount>({
    queryKey: ADMIN_AI_ACCOUNT_KEY,
    queryFn: fetchAdminAIAccount,
    retry: false,
  });
  const loginId = startedLoginId ?? account.data?.login?.login_id ?? null;
  // Polls only while the login is pending. An error stops it: the previous data
  // stays 'pending', and a login the server no longer knows (restart) answers
  // 404 on every retry.
  const login = useQuery<AdminAIDeviceLogin>({
    queryKey: loginKey(loginId),
    queryFn: () => fetchAdminAILogin(loginId ?? ''),
    enabled: loginId !== null,
    retry: false,
    refetchInterval: (query) =>
      query.state.status !== 'error' && query.state.data?.status === 'pending' ? POLL_LIVE : false,
  });
  const loginStatus = login.data?.status;

  // The account, runtime status and model catalog all describe the login.
  const refreshAccount = () => {
    for (const queryKey of [ADMIN_AI_ACCOUNT_KEY, ADMIN_AI_KEY, ADMIN_AI_MODELS_KEY]) {
      void queryClient.invalidateQueries({ queryKey });
    }
  };

  const startLogin = useMutation({
    mutationFn: () => startAdminAILogin({ type: 'chatgpt_device_code' }),
    onSuccess: (res) => {
      if (!('login_id' in res)) return;
      queryClient.setQueryData(loginKey(res.login_id), res);
      setStartedLoginId(res.login_id);
      setKeySaved(false);
    },
  });
  const cancelLogin = useMutation({
    mutationFn: cancelAdminAILogin,
    onSuccess: (res) => queryClient.setQueryData(loginKey(res.login_id), res),
  });
  const registerKey = useMutation({
    mutationFn: () => {
      const key = keyRef.current ?? '';
      keyRef.current = null;
      return startAdminAILogin({ type: 'api_key', api_key: key });
    },
    onSuccess: () => {
      setKeyFormOpen(false);
      setKeySaved(true);
      refreshAccount();
    },
    onError: (error) => setKeyError(error),
  });
  const logout = useMutation({
    mutationFn: logoutAdminAI,
    onSuccess: () => {
      setLogoutOpen(false);
      setKeySaved(false);
      refreshAccount();
    },
  });

  const handleStartLogin = () => {
    setKeyError(null);
    startLogin.mutate();
  };
  const handleCancelLogin = () => {
    if (loginId) cancelLogin.mutate(loginId);
  };
  // A second press closes the form, the same as 닫기 -- see the provider key row.
  const handleToggleKeyForm = () => {
    if (keyFormOpen) {
      setKeyFormOpen(false);
      return;
    }
    setKeyError(null);
    setKeySaved(false);
    setKeyFormOpen(true);
  };
  const handleCloseKeyForm = () => setKeyFormOpen(false);
  const handleRegisterKey = (key: string) => {
    keyRef.current = key;
    setKeyError(null);
    // Reset once settled, so nothing of the request stays in the mutation.
    registerKey.mutate(undefined, { onSettled: () => registerKey.reset() });
  };
  const handleOpenLogout = () => {
    logout.reset();
    setLogoutOpen(true);
  };
  const handleCloseLogout = () => setLogoutOpen(false);
  const handleLogout = () => logout.mutate();

  /** When the polled login ends, the account, runtime status and catalog still
   *  describe the login before it, so they are read again. */
  useEffect(() => {
    if (!loginStatus || loginStatus === 'pending') return;
    for (const queryKey of [ADMIN_AI_ACCOUNT_KEY, ADMIN_AI_KEY, ADMIN_AI_MODELS_KEY]) {
      void queryClient.invalidateQueries({ queryKey });
    }
  }, [loginStatus, queryClient]);

  if (account.isError) {
    return (
      <p className="text-[12px] text-danger">
        {accountErrorMessage(account.error instanceof AIReportRequestError ? account.error.code : '')}
      </p>
    );
  }
  if (!account.data) return null;

  const data = account.data;
  const lockReason = accountLockReason(data);
  const pendingLogin = loginStatus === 'pending' && !login.isError ? login.data : undefined;
  const busy = startLogin.isPending || registerKey.isPending || logout.isPending || Boolean(pendingLogin);
  const locked = lockReason !== null;
  const result = login.data && login.data.status !== 'pending' ? LOGIN_RESULT[login.data.status] : null;
  const resultDetail = loginErrorMessage(login.data?.error ?? null);
  const actionError = errorText(startLogin.error) ?? errorText(keyError) ?? errorText(cancelLogin.error);
  const accountLine = [authLabel[data.auth_mode] ?? data.auth_mode, data.email, data.plan_type].filter(Boolean).join(' · ');

  return (
    <section className="border-t border-border pt-3 space-y-2">
      <header className="flex items-baseline justify-between gap-3">
        <h4 className="text-[12px] font-medium text-ink">연결 계정</h4>
        <span className="text-[12px] text-ink-2 truncate">{accountLine}</span>
      </header>

      <div className="flex flex-wrap gap-2">
        <Button variant="outline" onClick={handleStartLogin} disabled={locked || busy} className={OUTLINE_BUTTON_CLASS}>
          ChatGPT로 로그인
        </Button>
        <Button variant="outline" onClick={handleToggleKeyForm} disabled={locked || busy} className={OUTLINE_BUTTON_CLASS}>
          API 키 등록
        </Button>
        <Button
          variant="outline"
          onClick={handleOpenLogout}
          disabled={locked || busy || data.auth_mode === 'none'}
          className={OUTLINE_BUTTON_CLASS}
        >
          로그아웃
        </Button>
      </div>

      {lockReason && <p className="text-[11px] text-ink-3">{lockReason}</p>}

      {pendingLogin && (
        <DeviceLoginPanel
          login={pendingLogin}
          checkedAt={login.dataUpdatedAt}
          canceling={cancelLogin.isPending}
          onCancel={handleCancelLogin}
        />
      )}
      {keyFormOpen && !locked && !pendingLogin && (
        <ApiKeyForm
          label="OpenAI API 키"
          placeholder="sk-..."
          pending={registerKey.isPending}
          onSubmit={handleRegisterKey}
          onClose={handleCloseKeyForm}
        />
      )}

      {result && (
        <p className={cn('text-[12px]', result.ok ? 'text-success' : 'text-ink-2')}>
          {result.text}
          {resultDetail && <span className="text-ink-3"> · {resultDetail}</span>}
        </p>
      )}
      {keySaved && <p className="text-[12px] text-success">API 키를 등록했습니다.</p>}
      {actionError && <p className="text-[12px] text-danger">{actionError}</p>}

      {logoutOpen && (
        <LogoutDialog
          pending={logout.isPending}
          errorMessage={errorText(logout.error)}
          onConfirm={handleLogout}
          onCancel={handleCloseLogout}
        />
      )}
    </section>
  );
};

export { ADMIN_AI_KEY, ADMIN_AI_MODELS_KEY, AccountSection, ApiKeyForm, OUTLINE_BUTTON_CLASS };
