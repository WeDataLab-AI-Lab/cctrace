'use client';

import { useState, useRef, useEffect } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { LogOut, User, PanelLeftClose, PanelLeftOpen, Square, SquareCheck, Menu, Search, SlidersHorizontal, X } from 'lucide-react';
import { Dialog as DialogPrimitive } from 'radix-ui';
import { fetchAccounts } from '@/lib/api';
import { withBasePath } from '@/lib/base-path';
import { cn } from '@/lib/utils';
import { WEEKLY_USAGE_HINT } from '@/lib/weekly-usage';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';
import { ScrollFadeRow } from '@/components/common/scroll-fade-row';
import { QuotaHeaderMeters } from '@/components/common/quota-header-meters';
import { Sidebar } from '@/components/common/sidebar';
import { SessionSearchBox } from '@/components/sessions/frame/session-search';
import type { SessionRecordSearchMatch, SessionRecordSearchResult } from '@/lib/session-analysis-types';
import { useAccount } from './account-context';
import { useAgent, type AgentScope } from './agent-context';
import { useAuth } from './auth-context';
import { useSessionLayout } from './session-layout-context';

const pageTitles: Record<string, string> = {
  '/': 'Overview',
  '/cost': 'Usage',
  '/weekly': 'Weekly report',
  '/users': 'Users',
  '/sessions': 'Sessions',
  '/tools': 'Tools',
  '/plugins': 'Plugins & Skills',
  '/skills': 'Plugins & Skills',
  '/rules': 'Rules',
  '/open-api': 'Open API',
  '/admin': 'Admin',
  '/logs': 'Logs',
  '/settings': 'Settings',
  '/versions': 'Client Versions',
  '/storage': 'Storage',
};

// Views that do not take an Account/Agent scope — hide those filters.
//
// Two reasons land in the same set. /versions, /storage and /open-api are admin/infra
// views that were never analytics. /weekly is analytics but its scope axis is the
// person (#664, adr-weekly-retrospective.md decision 4): it answers "what did I do
// this week", and the chips sat above it doing nothing, because the handler and every
// query behind it take the caller's identity and no scope parameter at all.
export const nonAnalyticsPaths = new Set(['/versions', '/storage', '/open-api', '/weekly']);

// The harness axis. 'Other' holds every harness that is not Claude Code or Codex —
// today gjc and omo — as one pill rather than one each, so the row does not grow with
// every tool we learn to read.
//
// Not to be confused with the billing filter, which also has a value called 'other'.
// That one is about who was paid; this one is about what program ran. A Claude Code
// session on a compatible model is 'Claude' here and 'other' there.
//
// These pills are the only harness selector. The sessions page used to carry a second
// one that looked identical and filtered nothing, because it read its own state
// instead of this context.
//
// 'weekly' is the server's weekly AI report runs, not a harness. It is lower-case
// because the charts name it the same way, and it carries a hover hint because the
// name alone does not say whose tokens they are.
const agentOptions: { value: AgentScope; label: string; dot?: string; hint?: string }[] = [
  { value: '', label: 'All' },
  { value: 'claude', label: 'Claude', dot: 'bg-agent-claude' },
  { value: 'codex', label: 'Codex', dot: 'bg-agent-codex' },
  { value: 'other', label: 'Other', dot: 'bg-agent-compat' },
  { value: 'weekly', label: 'weekly', dot: 'bg-agent-weekly', hint: WEEKLY_USAGE_HINT },
];

const pillClass = (active: boolean) =>
  cn(
    'shrink-0 inline-flex min-h-11 items-center gap-1.5 px-3 py-1 rounded-full text-xs font-medium transition-colors lg:min-h-0',
    active
      ? 'bg-brand text-brand-ink'
      : 'bg-surface text-ink-2 hover:bg-surface-sunk border border-border',
  );

const Header = () => {
  const rawPathname = usePathname();
  const router = useRouter();
  const pathname = rawPathname.replace(/\/+$/, '') || '/';
  const title = pageTitles[pathname] || 'Overview';
  // Account/Agent scope filters are analytics-only; hide them on admin/infra views.
  const showScopeFilters = !nonAnalyticsPaths.has(pathname);
  const { selectedAccount, setSelectedAccount, ownAccountsOnly, setOwnAccountsOnly } = useAccount();
  const { selectedAgent, setSelectedAgent } = useAgent();
  const { user, logout } = useAuth();
  const { menuCollapsed, toggleMenu } = useSessionLayout();
  // The server pins a role=user session list to the caller's own scope (#811), so chips there do nothing.
  const pinnedToOwnAccount = user?.role === 'user' && pathname === '/sessions';
  const mobileAccountSummary = pinnedToOwnAccount
    ? '본인 계정 전체'
    : selectedAccount.split('@')[0] || 'All accounts';
  const [showProfileMenu, setShowProfileMenu] = useState(false);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [mobileFiltersOpen, setMobileFiltersOpen] = useState(false);
  const [focusMobileSearch, setFocusMobileSearch] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const filtersRef = useRef<HTMLDivElement>(null);
  // The chips list every member's accounts because the screens below are shared. A role=user
  // viewer may trim them to their own; the server ignores that request for an admin.
  const canTrimToOwn = user?.role === 'user';
  const accountScope = canTrimToOwn && ownAccountsOnly ? 'mine' : 'all';
  const { data: accounts = [] } = useQuery<string[]>({
    // The user id keeps one viewer's 'mine' list from being served to the next one to sign in.
    queryKey: ['accounts', user?.id ?? null, accountScope],
    queryFn: () => fetchAccounts(accountScope),
    staleTime: 5 * 60 * 1000,
    enabled: !!user,
    // Flipping the toggle keeps the old chips until the other list arrives, so the row (and
    // the toggle in it) does not blink out in between.
    placeholderData: keepPreviousData,
  });
  // Trimmed to one account the row must stay, or the toggle that trimmed it would vanish too.
  const showAccountChips = !pinnedToOwnAccount && (accounts.length > 1 || accountScope === 'mine');

  const handleSelectAllAccounts = () => setSelectedAccount('');
  /** "Mine"을 켜면 선택된 계정 칩이 사라질 수 있으므로 선택을 All로 되돌린다. */
  const handleToggleOwnAccounts = () => {
    const next = !ownAccountsOnly;
    setOwnAccountsOnly(next);
    if (next) setSelectedAccount('');
  };
  const handleProfileToggle = () => setShowProfileMenu((v) => !v);
  const handleMobileFiltersToggle = () => setMobileFiltersOpen((open) => !open);
  const handleMobileSearchOpen = () => { setMobileFiltersOpen(true); setFocusMobileSearch(true); };
  const handleMobileNavigate = () => setMobileMenuOpen(false);
  const handleLogout = async () => {
    await logout();
    window.location.href = withBasePath('/login');
  };
  const handleSessionSearchSelect = (result: SessionRecordSearchResult, match: SessionRecordSearchMatch) => {
    const params = new URLSearchParams({ session_id: result.session_id, session_search: '1' });
    if (match.turn_key) params.set('turn_key', match.turn_key);
    router.push(`/sessions?${params.toString()}`);
  };
  const sessionSearchScope = user
    ? [user.id, user.role, user.cctrace_user_id ?? '', user.email, selectedAccount].join('|')
    : '';

  /** 프로필 메뉴 바깥을 클릭하면 메뉴를 닫는다. */
  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setShowProfileMenu(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  /** 경로가 바뀌면 모바일 탐색을 닫고 새 화면의 키보드 탐색을 허용한다. */
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setMobileMenuOpen(false);
  }, [pathname]);

  /** 검색 단축 버튼은 필터 영역의 기존 검색 입력으로 포커스를 옮긴다. */
  useEffect(() => {
    if (!focusMobileSearch || !mobileFiltersOpen) return;
    filtersRef.current?.querySelector<HTMLInputElement>('input[type="search"]')?.focus();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setFocusMobileSearch(false);
  }, [focusMobileSearch, mobileFiltersOpen]);

  /** 넓은 화면으로 전환되면 modal focus trap을 해제하고 데스크톱 메뉴를 사용한다. */
  useEffect(() => {
    if (!window.matchMedia) return;
    const desktop = window.matchMedia('(min-width: 1024px)');
    const closeOnDesktop = () => { if (desktop.matches) setMobileMenuOpen(false); };
    desktop.addEventListener('change', closeOnDesktop);
    return () => desktop.removeEventListener('change', closeOnDesktop);
  }, []);

  return (
    <DialogPrimitive.Root open={mobileMenuOpen} onOpenChange={setMobileMenuOpen}>
    <header className="flex min-w-0 shrink-0 flex-col gap-2 border-b border-border bg-surface px-4 py-2 lg:px-6">
      <div className="flex min-w-0 items-center gap-2">
        <DialogPrimitive.Trigger asChild>
          <button type="button" aria-label="앱 메뉴 열기" aria-controls="app-menu-mobile" className="inline-flex size-11 shrink-0 items-center justify-center rounded text-ink-3 hover:bg-surface-sunk lg:hidden"><Menu className="size-5" /></button>
        </DialogPrimitive.Trigger>
        <button type="button" aria-label={menuCollapsed ? '앱 메뉴 펼치기' : '앱 메뉴 접기'} aria-expanded={!menuCollapsed} aria-controls="app-menu" onClick={toggleMenu} className="hidden size-11 items-center justify-center rounded text-ink-3 hover:bg-surface-sunk hover:text-ink lg:inline-flex">
          {menuCollapsed ? <PanelLeftOpen className="size-4" /> : <PanelLeftClose className="size-4" />}
        </button>
        <h1 className="min-w-0 flex-1 truncate text-[16px] font-semibold text-ink">{title}</h1>
        {user && <button type="button" aria-label="전체 검색 열기" onClick={handleMobileSearchOpen} className="inline-flex size-11 shrink-0 items-center justify-center rounded text-ink-2 hover:bg-surface-sunk lg:hidden"><Search className="size-5" /></button>}
        {user && (
          <div className="relative shrink-0" ref={menuRef}>
            <button type="button" aria-label="프로필 메뉴" aria-expanded={showProfileMenu} onClick={handleProfileToggle} className="flex min-h-11 items-center gap-2 rounded-md px-2 text-sm text-ink-2 transition-colors hover:bg-surface-sunk">
              <User size={18} />
              <span className="hidden text-xs lg:inline">{user.name || user.email.split('@')[0]}</span>
            </button>
            {showProfileMenu && (
              <div className="absolute right-0 top-full z-50 mt-1 min-w-[160px] max-w-[calc(100vw-2rem)] rounded-md border border-border bg-surface py-1 shadow-[var(--sh-pop)]">
                <div className="border-b border-border px-3 py-2">
                  <p className="text-xs font-medium text-ink">{user.name}</p>
                  <p className="break-all text-[11px] text-ink-3">{user.email}</p>
                  <p className="mt-0.5 text-[10px] text-brand">{user.role}</p>
                </div>
                <button onClick={handleLogout} className="flex min-h-11 w-full items-center gap-2 px-3 py-2 text-xs text-ink-2 transition-colors hover:bg-surface-sunk lg:min-h-0"><LogOut size={14} />Sign out</button>
              </div>
            )}
          </div>
        )}
      </div>
      <div className="flex min-w-0 items-center gap-2 lg:hidden">
        <span className="min-w-0 flex-1 truncate rounded-lg bg-surface-sunk px-3 py-2.5 text-xs text-ink-2">
          {mobileAccountSummary} · {selectedAgent ? agentOptions.find((option) => option.value === selectedAgent)?.label : 'All agents'}
        </span>
        <button type="button" aria-label={mobileFiltersOpen ? '필터 닫기' : '필터 열기'} aria-expanded={mobileFiltersOpen} aria-controls="mobile-filters" onClick={handleMobileFiltersToggle} className="inline-flex size-11 shrink-0 items-center justify-center rounded-lg bg-surface-sunk text-ink-2"><SlidersHorizontal className="size-5" /></button>
      </div>
      <div id="mobile-filters" ref={filtersRef} className={cn('min-w-0 flex-wrap items-center justify-start gap-2 max-lg:max-h-[50dvh] max-lg:overflow-y-auto max-lg:overscroll-contain lg:flex lg:gap-4', mobileFiltersOpen ? 'flex' : 'hidden')}>
        {user && <div className="w-full lg:w-auto max-lg:[&>div]:w-full max-lg:[&_input]:h-11 max-lg:[&_input]:w-full max-lg:[&_[role=region]]:left-0 max-lg:[&_[role=region]]:right-auto"><SessionSearchBox key={sessionSearchScope} scopeKey={sessionSearchScope} onSelect={handleSessionSearchSelect} /></div>}
        <QuotaHeaderMeters enabled={!!user} />
        {showScopeFilters && (
        <>
        {accounts && accounts.length > 1 && pinnedToOwnAccount && (
          // On the session list the server pins a role=user request to the caller's own
          // user_id and drops login_email (#811), so pills here would do nothing. Say what
          // the view is. Other analytics handlers apply login_email as sent, so they keep
          // the pills below.
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <span className="shrink-0 text-[11px] font-medium text-ink-3">Account</span>
            <span className="text-[11px] text-ink-3">세션 목록은 본인 계정 전체 기준입니다</span>
          </div>
        )}
        {showAccountChips && (
          // The narrow filter panel wraps all account choices; the desktop row scrolls
          // so a long shared account list never pushes the next scope off screen.
          <div className={cn('flex min-w-0 flex-1 flex-wrap items-center gap-2 lg:flex-nowrap', canTrimToOwn && 'lg:min-w-56')}>
            <span className="shrink-0 text-[11px] font-medium text-ink-3">Account</span>
            <ScrollFadeRow className="max-lg:flex-wrap max-lg:overflow-visible">
              <button onClick={handleSelectAllAccounts} className={pillClass(selectedAccount === '')}>
                All
              </button>
              {accounts.map((account) => (
                <button
                  key={account}
                  onClick={() => setSelectedAccount(account)}
                  className={pillClass(selectedAccount === account)}
                >
                  {account.split('@')[0]}
                </button>
              ))}
            </ScrollFadeRow>
            {canTrimToOwn && (
              // The check box sets it apart from the account pills beside it: it is a switch, not an account.
              <button type="button" aria-pressed={ownAccountsOnly} onClick={handleToggleOwnAccounts} className={pillClass(ownAccountsOnly)}>
                {ownAccountsOnly ? <SquareCheck className="size-3.5" aria-hidden /> : <Square className="size-3.5" aria-hidden />}
                Mine
              </button>
            )}
          </div>
        )}

        {/* Agent scope toggle */}
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="shrink-0 text-[11px] font-medium text-ink-3">Agent</span>
          <TooltipProvider delayDuration={300}>
          <div className="flex flex-wrap gap-1 pb-1 lg:overflow-x-auto lg:pb-0">
            {agentOptions.map((opt) => {
              const active = selectedAgent === opt.value;
              const pill = (
                <button
                  key={opt.value || 'all'}
                  onClick={() => setSelectedAgent(opt.value)}
                  className={pillClass(active)}
                >
                  {opt.dot && (
                    <span
                      className={cn(
                        'h-1.5 w-1.5 rounded-full',
                        active ? 'bg-brand-ink' : opt.dot,
                      )}
                    />
                  )}
                  {opt.label}
                </button>
              );
              return opt.hint ? (
                <Tooltip key={opt.value}>
                  <TooltipTrigger asChild>{pill}</TooltipTrigger>
                  <TooltipContent className="max-w-[260px]">{opt.hint}</TooltipContent>
                </Tooltip>
              ) : pill;
            })}
          </div>
          </TooltipProvider>
        </div>
        </>
        )}

      </div>
    </header>
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay data-testid="mobile-menu-backdrop" className="fixed inset-0 z-50 bg-black/40 lg:hidden" />
      <DialogPrimitive.Content className="fixed inset-y-0 left-0 z-50 w-80 max-w-[calc(100vw-3rem)] bg-surface outline-none lg:hidden">
        <DialogPrimitive.Title className="sr-only">앱 메뉴</DialogPrimitive.Title>
        <div className="flex h-full flex-col">
          <div className="flex shrink-0 items-center justify-between px-4 py-2 text-sm text-ink-2">
            <span>Menu</span>
            <DialogPrimitive.Close className="inline-flex size-11 items-center justify-center rounded hover:bg-surface-sunk" aria-label="앱 메뉴 닫기"><X className="size-5" /></DialogPrimitive.Close>
          </div>
          <Sidebar mobile onNavigate={handleMobileNavigate} />
        </div>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
};

export { Header, agentOptions, pageTitles };
