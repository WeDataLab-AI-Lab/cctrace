'use client';

import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  LayoutDashboard,
  BarChart3,
  Users,
  Terminal,
  Wrench,
  ScrollText,
  Settings,
  Puzzle,
  FileText,
  ShieldCheck,
  BookOpen,
  CalendarDays,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { CctraceIcon } from '@/components/icons/cctrace-icon';
import { fetchAppVersion } from '@/lib/api';
import { resetFilters } from '@/lib/filter-reset';
import { useAppearance } from './appearance-context';
import { useAuth } from './auth-context';

// group drives the section header rendered above each run of items below — see
// the render loop, which starts a new header whenever group changes from the
// previous *visible* item (adminOnly items are filtered out first, so a
// non-admin viewer never sees a header with nothing under it).
const navItems = [
  { href: '/', label: 'Overview', icon: LayoutDashboard, group: 'ANALYTICS' },
  { href: '/cost', label: 'Usage', icon: BarChart3, group: 'ANALYTICS' },
  { href: '/users', label: 'Users', icon: Users, group: 'ANALYTICS' },
  { href: '/sessions', label: 'Sessions', icon: Terminal, group: 'ANALYTICS' },
  { href: '/weekly', label: 'Weekly report', icon: CalendarDays, group: 'ANALYTICS' },
  { href: '/tools', label: 'Tools', icon: Wrench, group: 'INVENTORY' },
  { href: '/plugins', label: 'Plugins & Skills', icon: Puzzle, group: 'INVENTORY' },
  { href: '/rules', label: 'Rules', icon: FileText, group: 'INVENTORY' },
  {
    href: '/open-api',
    label: 'Open API',
    icon: BookOpen,
    group: 'SYSTEM',
    children: [
      { href: '#getting-started', label: 'Getting Started' },
      { href: '#authentication', label: 'Authentication' },
      { href: '#usage', label: 'Usage' },
      { href: '#reference', label: 'Reference' },
      { href: '#errors', label: 'Errors & Limits' },
      { href: '#security', label: 'Security' },
      { href: '#troubleshooting', label: 'Troubleshooting' },
    ],
  },
  { href: '/logs', label: 'Logs', icon: ScrollText, group: 'SYSTEM' },
  { href: '/admin', label: 'Admin', icon: ShieldCheck, group: 'SYSTEM', adminOnly: true },
  { href: '/settings', label: 'Settings', icon: Settings, group: 'SYSTEM' },
];

const Sidebar = () => {
  const pathname = usePathname();
  const normalized = pathname.replace(/\/+$/, '') || '/';
  const { showLogoMark } = useAppearance();
  const { isAdmin, user } = useAuth();
  const [activeHash, setActiveHash] = useState('');
  const { data: appVersion } = useQuery({
    queryKey: ['app-version'],
    queryFn: fetchAppVersion,
    staleTime: Infinity,
  });

  // Every route but Settings bounces back while the password is temporary
  // (see the dashboard layout's redirect). Drawing those rows as live links
  // made the nav look responsive and act dead: a click changed nothing on
  // screen, with no message to explain it.
  const passwordLocked = Boolean(user?.must_change_password);

  // The logo is the one explicit "start over". Auto-refresh now preserves what the user is
  // looking at, so there has to be a deliberate way to drop the filters — and this is it.
  // Appearance and login are untouched: those are user settings, not a view of the data.
  const handleLogoClick = () => resetFilters();

  /** 현재 해시와 Open API 하위 문서 링크의 활성 상태를 동기화한다. */
  useEffect(() => {
    const syncHash = () => setActiveHash(window.location.hash);
    syncHash();
    window.addEventListener('hashchange', syncHash);
    return () => window.removeEventListener('hashchange', syncHash);
  }, [pathname]);

  return (
    <aside className="hidden w-[248px] min-w-[248px] min-h-screen bg-surface border-r border-border flex-col md:flex">
      <Link
        href="/"
        onClick={handleLogoClick}
        className="block px-4 py-[18px] hover:bg-surface-sunk transition-colors"
      >
        {/* No divider under the lockup: the name and the nav below it are one
            block, and a rule there cut the sidebar into two headers.

            The whole lockup is a ramp, mark and name together, so the eye lands
            on the name rather than on the mark. It rises from 5% at the bottom
            left to 90% at the top right: the mark sits in the faint end and
            stays a texture, the name arrives near full ink.

            The diagonal is the point of the direction. A left-to-right ramp put
            equal weight on both lines of the name; running it to the top right
            lifts "cctrace" clear of "Analytics Engine" without setting a second
            colour or weight on either.

            `w-fit` matters: the span is a flex child of a block, so without it the
            box stretches the full sidebar width and roughly a quarter of the ramp
            lands on empty space past the end of the name -- the word then tops out
            around two thirds of the way instead of reaching the value the
            gradient names. With a diagonal the same applies to the height, which
            the two-line block already sets.

            The two lines are set solid. `Analytics Engine` was inheriting the
            body's 1.65, which wrapped a 10px word in a 16.5px box -- most of the
            gap between the lines was that empty box, not a spacing decision. Both
            lines now state their own leading and the 3px between them is the whole
            of the gap.

            The ramp is a mask, not an opacity: mask alpha IS the resulting
            opacity, so the two numbers in the gradient are the two ends of the
            fade with nothing to multiply out. A mask also covers the text and the
            SVG in one pass -- a colour ramp would need background-clip for one and
            a gradient fill for the other. And it sits on this span rather than the
            Link because masking the Link would take the hover fill with it and
            cost the row its feedback.

            The mark centres on the two-line block, not on the name alone, so the
            lockup reads as one object. `items-center` lines the boxes up and the
            2px carries the mark down onto the block's ink centre -- the text box
            has empty ascender room above and none below, so its drawn extent sits
            lower than its box.

            That 2px was measured against the old leading (residual 0.19px), and
            setting both lines solid moves the box it was correcting. It is kept
            rather than guessed at a new value: re-deriving it needs a rendered
            measurement, and a number invented here would read as measured. Check
            it on screen and re-measure if the mark reads low. */}
        <span className="flex w-fit items-center gap-0.5 [-webkit-mask-image:linear-gradient(to_top_right,rgba(0,0,0,0.05)_0%,rgba(0,0,0,0.90)_100%)] [mask-image:linear-gradient(to_top_right,rgba(0,0,0,0.05)_0%,rgba(0,0,0,0.90)_100%)]">
          {showLogoMark && <CctraceIcon size={52} className="translate-y-[2px]" />}
          <span>
            <span className="block text-[20px] font-semibold leading-none text-ink tracking-[-0.02em]">cctrace</span>
            <span className="mt-[3px] block text-[10px] font-semibold uppercase leading-[1.1] tracking-[0.09em] text-ink-3">
              Analytics Engine
            </span>
          </span>
        </span>
      </Link>

      <nav className="flex flex-col gap-1.5 px-4 pt-3.5">
        {navItems.filter((i) => !('adminOnly' in i) || isAdmin).map(({ href, label, icon: Icon, group, ...item }, index, visible) => {
          const active = normalized === href;
          const locked = passwordLocked && href !== '/settings';
          const showGroupHeader = index === 0 || visible[index - 1].group !== group;
          return (
            <div key={href}>
              {showGroupHeader && (
                <p
                  className={cn(
                    'text-[11px] font-semibold text-ink-3 tracking-[0.05em] uppercase mb-1 px-3',
                    index === 0 ? '' : 'mt-6',
                  )}
                >
                  {group}
                </p>
              )}
              {locked ? (
                <span
                  aria-disabled="true"
                  title="Change your temporary password to open this page"
                  className="relative flex cursor-not-allowed items-center gap-3 rounded-md px-3 py-3 text-[13px] text-ink-3"
                >
                  <Icon size={18} />
                  {label}
                </span>
              ) : (
                <Link
                  href={href}
                  className={cn(
                    'relative flex items-center gap-3 px-3 py-3 rounded-md text-[13px] transition-colors',
                    active
                      ? 'bg-brand-soft text-brand font-medium'
                      : 'text-ink-2 hover:bg-surface-sunk',
                  )}
                >
                  {active && (
                    <span className="absolute left-0 top-1/2 -translate-y-1/2 h-5 w-[2px] rounded-full bg-brand" />
                  )}
                  <Icon size={18} />
                  {label}
                </Link>
              )}
              {'children' in item && item.children && active && (
                <div className="ml-5 mt-1 border-l border-border pl-4">
                  {item.children.map((child) => {
                    const childActive = activeHash === child.href;
                    return (
                      <a
                        key={child.href}
                        href={child.href}
                        aria-current={childActive ? 'location' : undefined}
                        className={cn(
                          'relative block rounded-md px-3 py-1.5 text-[12px] transition-colors',
                          childActive
                            ? 'bg-brand-soft font-medium text-brand'
                            : 'text-ink-3 hover:bg-surface-sunk hover:text-ink-2',
                        )}
                      >
                        {childActive && (
                          <span className="absolute -left-[17px] top-1/2 h-1.5 w-1.5 -translate-y-1/2 rounded-full bg-brand" />
                        )}
                        {child.label}
                      </a>
                    );
                  })}
                </div>
              )}
            </div>
          );
        })}
      </nav>

      <div className="mt-auto px-4 py-3 text-[11px] text-ink-3">
        {appVersion ? `cctrace ${appVersion}` : 'cctrace'}
      </div>
    </aside>
  );
};

export { Sidebar, navItems };
