'use client';

import { useState, useRef, useEffect } from 'react';
import { usePathname } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { LogOut, User } from 'lucide-react';
import { fetchAccounts } from '@/lib/api';
import { cn } from '@/lib/utils';
import { WEEKLY_USAGE_HINT } from '@/lib/weekly-usage';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';
import { ScrollFadeRow } from '@/components/common/scroll-fade-row';
import { useAccount } from './account-context';
import { useAgent, type AgentScope } from './agent-context';
import { useAuth } from './auth-context';

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
    'shrink-0 inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-medium transition-colors',
    active
      ? 'bg-brand text-brand-ink'
      : 'bg-surface text-ink-2 hover:bg-surface-sunk border border-border',
  );

const Header = () => {
  const rawPathname = usePathname();
  const pathname = rawPathname.replace(/\/+$/, '') || '/';
  const title = pageTitles[pathname] || 'Overview';
  // Account/Agent scope filters are analytics-only; hide them on admin/infra views.
  const showScopeFilters = !nonAnalyticsPaths.has(pathname);
  const { selectedAccount, setSelectedAccount } = useAccount();
  const { selectedAgent, setSelectedAgent } = useAgent();
  const { user, logout } = useAuth();
  // The server pins a role=user session list to the caller's own scope (#811), so chips there do nothing.
  const pinnedToOwnAccount = user?.role === 'user' && pathname === '/sessions';
  const [showProfileMenu, setShowProfileMenu] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const { data: accounts = [] } = useQuery<string[]>({
    queryKey: ['accounts'],
    queryFn: fetchAccounts,
    staleTime: 5 * 60 * 1000,
    enabled: !!user,
  });

  const handleSelectAllAccounts = () => setSelectedAccount('');
  const handleProfileToggle = () => setShowProfileMenu((v) => !v);
  const handleLogout = async () => {
    await logout();
    window.location.href = '/login';
  };

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

  return (
    // gap-3 is enough when the filters sit on their own line. Once the account row
    // takes flex-1 it starts immediately beside the title, so the two run together
    // on a single line -- the wider gap is only needed where that happens.
    <header className="flex min-h-14 flex-wrap items-center justify-between gap-3 border-b border-border bg-surface px-4 py-3 md:gap-6 md:px-6 md:py-0">
      <h1 className="shrink-0 text-[16px] font-semibold text-ink">{title}</h1>
      <div className="flex min-w-0 flex-1 flex-wrap items-center justify-start gap-2 md:justify-end md:gap-4">
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
        {accounts && accounts.length > 1 && !pinnedToOwnAccount && (
          // flex-1 so the group claims the width of the line it wrapped onto, and
          // min-w-0 so it is allowed to be narrower than its pills. Without both the
          // row sizes itself to its contents and runs off the edge instead of
          // scrolling -- twelve accounts was enough to show that.
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <span className="shrink-0 text-[11px] font-medium text-ink-3">Account</span>
            <ScrollFadeRow>
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
          </div>
        )}

        {/* Agent scope toggle */}
        <div className="flex min-w-0 items-center gap-2">
          <span className="shrink-0 text-[11px] font-medium text-ink-3">Agent</span>
          <TooltipProvider delayDuration={300}>
          <div className="flex gap-1 overflow-x-auto pb-1 md:pb-0">
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

        {/* Profile dropdown */}
        {user && (
          <div className="relative shrink-0" ref={menuRef}>
            <button
              onClick={handleProfileToggle}
              className="flex items-center gap-2 px-2 py-1 rounded-md text-sm text-ink-2 hover:bg-surface-sunk transition-colors"
            >
              <User size={16} />
              <span className="text-xs">{user.name || user.email.split('@')[0]}</span>
            </button>
            {showProfileMenu && (
              <div className="absolute right-0 top-full mt-1 bg-surface border border-border rounded-md shadow-[var(--sh-pop)] py-1 min-w-[160px] z-50">
                <div className="px-3 py-2 border-b border-border">
                  <p className="text-xs font-medium text-ink">{user.name}</p>
                  <p className="text-[11px] text-ink-3">{user.email}</p>
                  <p className="text-[10px] text-brand mt-0.5">{user.role}</p>
                </div>
                <div className="border-t border-border">
                  <button
                    onClick={handleLogout}
                    className="w-full flex items-center gap-2 px-3 py-2 text-xs text-ink-2 hover:bg-surface-sunk transition-colors"
                  >
                    <LogOut size={14} />
                    Sign out
                  </button>
                </div>
              </div>
            )}
          </div>
        )}
      </div>
    </header>
  );
};

export { Header, agentOptions, pageTitles };
