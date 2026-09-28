'use client';

import Link from 'next/link';
import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { CalendarClock, Check, ChevronDown, Clipboard, ExternalLink, KeyRound, Plus, RotateCw, ShieldOff } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { Input } from '@/components/ui/input';
import { useAuth } from '@/components/common/auth-context';
import {
  createOwnAPIToken,
  listOwnAPITokens,
  revokeOwnAPIToken,
  rotateOwnAPIToken,
  setOwnAPITokenActive,
  setOwnAPITokenExpiration,
} from '@/lib/api';
import type { APITokenInfo, APITokenSecret } from '@/lib/types';

interface PendingAction {
  type: 'rotate' | 'revoke';
  token: APITokenInfo;
}

interface IssuedSecret {
  name: string;
  value: string;
}

interface TokenRowProps {
  token: APITokenInfo;
  disabled: boolean;
  onRotate: (token: APITokenInfo) => void;
  onRevoke: (token: APITokenInfo) => void;
  onToggleActive: (token: APITokenInfo) => void;
  onEditExpiration: (token: APITokenInfo) => void;
}

type ExpirationMode = 'unlimited' | 'custom';

const TOKEN_LIST_QUERY_KEY = ['own-api-tokens'] as const;
const DATE_FORMAT = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });

const formatDate = (value: string | null) => value ? DATE_FORMAT.format(new Date(value)) : 'Never';
const toDatetimeLocal = (value: string) => {
  const date = new Date(value);
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
};

const TokenRow = ({ token, disabled, onRotate, onRevoke, onToggleActive, onEditExpiration }: TokenRowProps) => {
  const handleRotate = () => onRotate(token);
  const handleRevoke = () => onRevoke(token);
  const handleToggleActive = () => onToggleActive(token);
  const handleEditExpiration = () => onEditExpiration(token);
  const status = token.is_active ? 'Active' : 'Inactive';

  return (
    <article className="rounded-lg border border-border-subtle bg-surface-2 p-4">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-[13px] font-semibold text-ink">{token.name}</h3>
            <span className="rounded-full bg-surface-sunk px-2 py-0.5 text-[10px] font-medium uppercase tracking-[0.04em] text-ink-3">
              {tokenPurposeLabel(token.created_via)}
            </span>
            <span className={token.is_active
              ? 'rounded-full bg-success-soft px-2 py-0.5 text-[10px] font-medium text-success-strong'
              : 'rounded-full bg-surface-sunk px-2 py-0.5 text-[10px] font-medium text-ink-3'}
            >
              {status}
            </span>
            {token.is_expired && (
              <span className="rounded-full bg-warning-soft px-2 py-0.5 text-[10px] font-medium text-warning-strong">
                Expired
              </span>
            )}
          </div>
          <code className="mt-1 block font-mono text-[12px] text-ink-3">{token.token_hint}</code>
        </div>
        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={handleToggleActive}
            disabled={disabled}
            aria-pressed={token.is_active}
            aria-label={`${token.is_active ? 'Deactivate' : 'Activate'} ${token.name}`}
            className="group inline-flex h-8 items-center gap-2 rounded-md px-1.5 text-[11px] font-medium text-ink-3 transition-colors hover:text-ink focus-visible:ring-2 focus-visible:ring-brand/30 disabled:pointer-events-none disabled:opacity-50"
          >
            <span>{status}</span>
            <span className={token.is_active
              ? 'relative h-5 w-9 rounded-full bg-success transition-colors duration-200'
              : 'relative h-5 w-9 rounded-full bg-border-strong transition-colors duration-200 group-hover:bg-ink-4'}
            >
              <span className={token.is_active
                ? 'absolute left-0.5 top-0.5 size-4 translate-x-4 rounded-full bg-surface shadow-[var(--sh-xs)] transition-transform duration-200'
                : 'absolute left-0.5 top-0.5 size-4 translate-x-0 rounded-full bg-surface shadow-[var(--sh-xs)] transition-transform duration-200'}
              />
            </span>
          </button>

          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={disabled}
                className="bg-surface text-[12px] text-ink-2 shadow-[var(--sh-xs)] hover:border-border-strong hover:text-ink data-[state=open]:border-border-strong data-[state=open]:bg-surface-sunk"
              >
                Manage
                <ChevronDown className="size-3.5 text-ink-3" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-44 rounded-lg border-border p-1.5 shadow-[var(--sh-pop)]">
              <DropdownMenuItem onSelect={handleEditExpiration} className="rounded-md px-2.5 py-2 text-[12px]">
                <CalendarClock />
                Expiration
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={handleRotate} className="rounded-md px-2.5 py-2 text-[12px]">
                <RotateCw />
                Rotate token
              </DropdownMenuItem>
              <DropdownMenuSeparator className="my-1" />
              <DropdownMenuItem
                variant="destructive"
                onSelect={handleRevoke}
                className="rounded-md px-2.5 py-2 text-[12px]"
              >
                <ShieldOff />
                Revoke token
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
      <dl className="mt-4 grid gap-3 border-t border-border-subtle pt-3 text-[11px] sm:grid-cols-3">
        <div>
          <dt className="text-ink-3">Created</dt>
          <dd className="mt-0.5 font-mono text-ink-2">{formatDate(token.created_at)}</dd>
        </div>
        <div>
          <dt className="text-ink-3">Last rotated</dt>
          <dd className="mt-0.5 font-mono text-ink-2">{formatDate(token.rotated_at)}</dd>
        </div>
        <div>
          <dt className="text-ink-3">Expires</dt>
          <dd className="mt-0.5 font-mono text-ink-2">{token.expires_at ? formatDate(token.expires_at) : 'Unlimited'}</dd>
        </div>
      </dl>
    </article>
  );
};

// cli_read tokens come from `cctrace auth read` and read the API like web ones.
const tokenPurposeLabel = (createdVia: APITokenInfo['created_via']): string => {
  switch (createdVia) {
    case 'web':
      return 'Read API';
    case 'cli_read':
      return 'Read API (CLI)';
    default:
      return 'CLI ingestion';
  }
};

const APITokenManagement = () => {
  const queryClient = useQueryClient();
  // The server refuses every token route until the temporary password is
  // changed (#559). Offering the form anyway meant the refusal arrived after
  // the name and the expiry were already filled in.
  const { user } = useAuth();
  const passwordLocked = Boolean(user?.must_change_password);
  const [showCreateForm, setShowCreateForm] = useState(false);
  const [tokenName, setTokenName] = useState('');
  const [createExpirationMode, setCreateExpirationMode] = useState<ExpirationMode>('unlimited');
  const [createExpiresAt, setCreateExpiresAt] = useState('');
  const [editingExpirationToken, setEditingExpirationToken] = useState<APITokenInfo | null>(null);
  const [editExpirationMode, setEditExpirationMode] = useState<ExpirationMode>('unlimited');
  const [editExpiresAt, setEditExpiresAt] = useState('');
  const [pendingAction, setPendingAction] = useState<PendingAction | null>(null);
  const [issuedSecret, setIssuedSecret] = useState<IssuedSecret | null>(null);
  const [copied, setCopied] = useState(false);
  const [minimumExpiration] = useState(() => toDatetimeLocal(new Date(Date.now() + 60_000).toISOString()));

  const tokensQuery = useQuery({
    queryKey: TOKEN_LIST_QUERY_KEY,
    queryFn: listOwnAPITokens,
    staleTime: Infinity,
  });

  const handleSecretIssued = (result: APITokenSecret) => {
    setIssuedSecret({ name: result.name, value: result.api_token });
    setCopied(false);
    setPendingAction(null);
    void queryClient.invalidateQueries({ queryKey: TOKEN_LIST_QUERY_KEY });
  };

  const createMutation = useMutation({
    mutationFn: ({ name, expiresAt }: { name: string; expiresAt: string | null }) => createOwnAPIToken(name, expiresAt),
    onSuccess: (result) => {
      handleSecretIssued(result);
      setTokenName('');
      setCreateExpirationMode('unlimited');
      setCreateExpiresAt('');
      setShowCreateForm(false);
    },
  });

  const rotateMutation = useMutation({
    mutationFn: rotateOwnAPIToken,
    onSuccess: handleSecretIssued,
  });

  const revokeMutation = useMutation({
    mutationFn: revokeOwnAPIToken,
    onSuccess: () => {
      if (issuedSecret?.name === pendingAction?.token.name) setIssuedSecret(null);
      setPendingAction(null);
      void queryClient.invalidateQueries({ queryKey: TOKEN_LIST_QUERY_KEY });
    },
  });

  const activeMutation = useMutation({
    mutationFn: ({ id, active }: { id: number; active: boolean }) => setOwnAPITokenActive(id, active),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: TOKEN_LIST_QUERY_KEY });
    },
  });

  const expirationMutation = useMutation({
    mutationFn: ({ id, expiresAt }: { id: number; expiresAt: string | null }) => setOwnAPITokenExpiration(id, expiresAt),
    onSuccess: () => {
      setEditingExpirationToken(null);
      void queryClient.invalidateQueries({ queryKey: TOKEN_LIST_QUERY_KEY });
    },
  });

  const isPending = createMutation.isPending || rotateMutation.isPending || revokeMutation.isPending
    || activeMutation.isPending || expirationMutation.isPending;
  const mutationError = createMutation.error ?? rotateMutation.error ?? revokeMutation.error
    ?? activeMutation.error ?? expirationMutation.error;
  const handleShowCreateForm = () => setShowCreateForm(true);
  const handleHideCreateForm = () => {
    setShowCreateForm(false);
    setTokenName('');
    setCreateExpirationMode('unlimited');
    setCreateExpiresAt('');
  };
  const handleTokenNameChange = (event: React.ChangeEvent<HTMLInputElement>) => setTokenName(event.target.value);
  const handleCreateExpirationModeChange = (event: React.ChangeEvent<HTMLSelectElement>) => {
    const value = event.target.value;
    if (value === 'unlimited' || value === 'custom') setCreateExpirationMode(value);
  };
  const handleCreateExpiresAtChange = (event: React.ChangeEvent<HTMLInputElement>) => setCreateExpiresAt(event.target.value);
  const handleCreate = (event: React.FormEvent) => {
    event.preventDefault();
    const name = tokenName.trim();
    if (!name) return;
    const expiresAt = createExpirationMode === 'custom' ? new Date(createExpiresAt).toISOString() : null;
    createMutation.mutate({ name, expiresAt });
  };
  const handleRequestRotate = (token: APITokenInfo) => setPendingAction({ type: 'rotate', token });
  const handleRequestRevoke = (token: APITokenInfo) => setPendingAction({ type: 'revoke', token });
  const handleToggleActive = (token: APITokenInfo) => activeMutation.mutate({ id: token.id, active: !token.is_active });
  const handleEditExpiration = (token: APITokenInfo) => {
    setEditingExpirationToken(token);
    setEditExpirationMode(token.expires_at ? 'custom' : 'unlimited');
    setEditExpiresAt(token.expires_at ? toDatetimeLocal(token.expires_at) : '');
  };
  const handleEditExpirationModeChange = (event: React.ChangeEvent<HTMLSelectElement>) => {
    const value = event.target.value;
    if (value === 'unlimited' || value === 'custom') setEditExpirationMode(value);
  };
  const handleEditExpiresAtChange = (event: React.ChangeEvent<HTMLInputElement>) => setEditExpiresAt(event.target.value);
  const handleCancelExpirationEdit = () => setEditingExpirationToken(null);
  const handleSaveExpiration = (event: React.FormEvent) => {
    event.preventDefault();
    if (!editingExpirationToken) return;
    const expiresAt = editExpirationMode === 'custom' ? new Date(editExpiresAt).toISOString() : null;
    expirationMutation.mutate({ id: editingExpirationToken.id, expiresAt });
  };
  const handleCancelConfirmation = () => setPendingAction(null);
  const handleConfirmAction = () => {
    if (!pendingAction) return;
    if (pendingAction.type === 'rotate') {
      rotateMutation.mutate(pendingAction.token.id);
      return;
    }
    revokeMutation.mutate(pendingAction.token.id);
  };
  const handleCopy = async () => {
    if (!issuedSecret) return;
    try {
      await navigator.clipboard.writeText(issuedSecret.value);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };

  return (
    <section className="mb-5 rounded-lg border border-border bg-surface p-8">
      <header className="mb-6 flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="mb-1 flex items-center gap-2.5">
            <KeyRound size={18} className="text-brand" />
            <h2 className="text-[16px] font-semibold text-ink">API Access Tokens</h2>
          </div>
          <p className="ml-[30px] text-[12px] text-ink-3">
            Tokens created here are read-only and cannot upload sync or OTLP data. For collection, run cctrace init to obtain a CLI ingestion token.
          </p>
          {passwordLocked && (
            <p className="ml-[30px] mt-1 text-[12px] text-warning-strong">
              Change your temporary password below before creating or rotating a token.
            </p>
          )}
        </div>
        {!showCreateForm && (
          <Button
            type="button"
            size="sm"
            onClick={handleShowCreateForm}
            disabled={isPending || passwordLocked}
            title={passwordLocked ? 'Change your temporary password below to manage tokens' : undefined}
          >
            <Plus />
            Create token
          </Button>
        )}
      </header>

      {showCreateForm && (
        <form onSubmit={handleCreate} className="mb-4 rounded-lg border border-brand/25 bg-brand-soft p-4">
          <label htmlFor="api-token-name" className="text-[12px] font-medium text-ink">Token name</label>
          <p className="mt-0.5 text-[11px] text-ink-3">Use the client or integration name, for example “CI deployment”.</p>
          <div className="mt-3 flex flex-wrap gap-2">
            <Input
              id="api-token-name"
              value={tokenName}
              onChange={handleTokenNameChange}
              maxLength={64}
              required
              autoFocus
              className="min-w-[220px] flex-1 bg-surface"
            />
            <select
              aria-label="Token expiration"
              value={createExpirationMode}
              onChange={handleCreateExpirationModeChange}
              className="h-8 rounded-md border border-border bg-surface px-3 text-[12px] text-ink outline-none focus:border-brand"
            >
              <option value="unlimited">Unlimited</option>
              <option value="custom">Custom expiration</option>
            </select>
            {createExpirationMode === 'custom' && (
              <Input
                type="datetime-local"
                aria-label="Expiration date and time"
                value={createExpiresAt}
                onChange={handleCreateExpiresAtChange}
                min={minimumExpiration}
                required
                className="w-auto bg-surface"
              />
            )}
            <Button type="submit" size="sm" disabled={isPending || !tokenName.trim() || (createExpirationMode === 'custom' && !createExpiresAt)}>Create</Button>
            <Button type="button" size="sm" variant="outline" onClick={handleHideCreateForm} disabled={isPending}>Cancel</Button>
          </div>
        </form>
      )}

      {editingExpirationToken && (
        <form onSubmit={handleSaveExpiration} className="mb-4 rounded-lg border border-brand/25 bg-brand-soft p-4">
          <p className="text-[13px] font-medium text-ink">Expiration for “{editingExpirationToken.name}”</p>
          <div className="mt-3 flex flex-wrap gap-2">
            <select
              aria-label="Edit token expiration"
              value={editExpirationMode}
              onChange={handleEditExpirationModeChange}
              className="h-8 rounded-md border border-border bg-surface px-3 text-[12px] text-ink outline-none focus:border-brand"
            >
              <option value="unlimited">Unlimited</option>
              <option value="custom">Custom expiration</option>
            </select>
            {editExpirationMode === 'custom' && (
              <Input
                type="datetime-local"
                aria-label="Updated expiration date and time"
                value={editExpiresAt}
                onChange={handleEditExpiresAtChange}
                min={minimumExpiration}
                required
                className="w-auto bg-surface"
              />
            )}
            <Button type="submit" size="sm" disabled={isPending || (editExpirationMode === 'custom' && !editExpiresAt)}>Save</Button>
            <Button type="button" size="sm" variant="outline" onClick={handleCancelExpirationEdit} disabled={isPending}>Cancel</Button>
          </div>
        </form>
      )}

      {issuedSecret && (
        <div className="mb-4 rounded-lg border border-success/30 bg-success-soft p-4" aria-live="polite">
          <div className="flex items-center gap-2 text-[13px] font-medium text-success-strong">
            <Check size={15} />
            {issuedSecret.name}: copy this token now. It will not be shown again.
          </div>
          <div className="mt-3 flex items-stretch gap-2">
            <code className="min-w-0 flex-1 select-all overflow-x-auto rounded-md border border-success/20 bg-surface px-3 py-2 font-mono text-[12px] text-ink">
              {issuedSecret.value}
            </code>
            <Button type="button" variant="outline" size="sm" onClick={handleCopy} className="h-auto bg-surface">
              {copied ? <Check /> : <Clipboard />}
              {copied ? 'Copied' : 'Copy'}
            </Button>
          </div>
        </div>
      )}

      {pendingAction && (
        <div className="mb-4 rounded-lg border border-warning/40 bg-warning-soft p-4" role="alert">
          <p className="text-[13px] font-medium text-warning-strong">
            {pendingAction.type === 'rotate' ? 'Rotate' : 'Revoke'} “{pendingAction.token.name}”?
          </p>
          <p className="mt-1 text-[12px] leading-5 text-warning-strong/80">
            The current secret will stop working immediately.
            {pendingAction.type === 'rotate' && ' The replacement will be shown only once.'}
          </p>
          <div className="mt-3 flex gap-2">
            <Button type="button" size="sm" onClick={handleConfirmAction} disabled={isPending}>
              {isPending ? 'Updating…' : 'Confirm'}
            </Button>
            <Button type="button" size="sm" variant="outline" onClick={handleCancelConfirmation} disabled={isPending}>Cancel</Button>
          </div>
        </div>
      )}

      {tokensQuery.isPending && <p className="text-[12px] text-ink-3">Loading tokens…</p>}
      {tokensQuery.data?.length === 0 && (
        <div className="rounded-lg border border-dashed border-border p-6 text-center text-[12px] text-ink-3">
          No API tokens yet. Create one for each application that needs access.
        </div>
      )}
      {tokensQuery.data && tokensQuery.data.length > 0 && (
        <div className="space-y-3">
          {tokensQuery.data.map((token) => (
            <TokenRow
              key={token.id}
              token={token}
              disabled={isPending}
              onRotate={handleRequestRotate}
              onRevoke={handleRequestRevoke}
              onToggleActive={handleToggleActive}
              onEditExpiration={handleEditExpiration}
            />
          ))}
        </div>
      )}

      {(tokensQuery.isError || mutationError) && (
        <p className="mt-4 text-[12px] text-danger" role="alert">
          {mutationError instanceof Error ? mutationError.message : 'Failed to load API tokens.'}
        </p>
      )}

      <p className="mt-4 text-[12px] leading-5 text-ink-3">
        Use a token as <code className="rounded bg-surface-sunk px-1 font-mono text-ink-2">Authorization: Bearer cct_…</code>.
        {' '}See the{' '}
        <Link href="/open-api#getting-started" className="inline-flex items-center gap-1 font-medium text-brand hover:underline">
          Open API manual <ExternalLink size={12} />
        </Link>
        {' '}for examples and endpoint reference.
      </p>
    </section>
  );
};

export { APITokenManagement, tokenPurposeLabel };
