'use client';

import React from 'react';
import { ChevronDown, RefreshCw, Trash2 } from 'lucide-react';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { formatRelativeTime } from '@/lib/format';
import type { ViewMode } from '@/components/common/trend-chart';
import type { ModelCategory } from '@/lib/colors';
import { modelLabel } from '@/lib/model-label';

interface DropdownOption {
  value: string;
  label: string;
}

interface DropdownProps {
  value: string;
  onChange: (v: string) => void;
  options: DropdownOption[];
  placeholder: string;
  mono?: boolean;
}

const Dropdown = ({ value, onChange, options, placeholder, mono }: DropdownProps) => {
  const selected = options.find((o) => o.value === value);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button" className={cn('flex h-[34px] max-w-[calc(100vw-2rem)] items-center gap-1.5 rounded-lg border border-border bg-surface px-3 text-[13px] text-ink-2 transition-colors data-[state=open]:border-brand', mono && 'font-mono')}>
          <span className="min-w-0 truncate">{selected?.label ?? (value || placeholder)}</span>
          <ChevronDown size={12} className="shrink-0" aria-hidden />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="max-h-60 min-w-[var(--radix-dropdown-menu-trigger-width)] max-w-[calc(100vw-2rem)] overflow-y-auto">
        {options.filter((option) => option.value !== value).map((option) =>
          <DropdownChoice key={option.value} option={option} onChange={onChange} mono={mono} />)}
      </DropdownMenuContent>
    </DropdownMenu>
  );
};

const DropdownChoice = ({ option, onChange, mono }: { option: DropdownOption; onChange: (value: string) => void; mono?: boolean }) => {
  const handleSelect = () => onChange(option.value);
  return <DropdownMenuItem onSelect={handleSelect} className={cn('min-h-11 max-w-full text-[13px] [overflow-wrap:anywhere] lg:min-h-0', mono && 'font-mono')}>{option.label}</DropdownMenuItem>;
};

export interface FilterBarProps {
  // Cost/Token toggle
  viewMode?: ViewMode;
  onViewModeChange?: (mode: ViewMode) => void;
  showViewToggle?: boolean;

  // All/Anthropic/Compatible toggle
  modelCategory?: ModelCategory;
  onModelCategoryChange?: (cat: ModelCategory) => void;
  showModelCategory?: boolean;

  // by Model/by User toggle
  groupBy?: 'model' | 'user';
  onGroupByChange?: (g: 'model' | 'user') => void;
  showGroupBy?: boolean;

  // Refresh button
  onRefresh?: () => void;
  showRefresh?: boolean;

  // Search input
  search?: string;
  onSearchChange?: (s: string) => void;
  searchPlaceholder?: string;
  showSearch?: boolean;

  // Project filter
  projects?: {
    project_hash: string;
    project_name: string;
    last_session_at?: string | null;
    /**
     * The real project_hash values this row stands for. A row is an identity (#304),
     * so one line can cover several hashes -- the same repository opened from
     * different worktrees. Deleting the row has to reach all of them.
     */
    member_hashes?: string[];
  }[];
  selectedProject?: string;
  onProjectChange?: (hash: string) => void;
  showProjectFilter?: boolean;
  /**
   * Keep every control on one line, shrinking them instead of wrapping. Off by
   * default: pages that show the full set of filters have more controls than fit
   * on any line, and squeezing those to nothing is worse than a second row.
   */
  nowrap?: boolean;
  /** Draws a delete control on each project row. Omitted when the caller may not delete. */
  onProjectDelete?: (project: { project_hash: string; project_name: string; member_hashes?: string[] }) => void;

  // Model filter dropdown
  modelFilter?: string;
  onModelFilterChange?: (filter: string) => void;
  models?: string[];
  showModelFilter?: boolean;

  // User filter dropdown
  userFilter?: string;
  onUserFilterChange?: (filter: string) => void;
  users?: string[];
  showUserFilter?: boolean;
  nameMap?: Record<string, string>;

  // Extra content slot
  children?: React.ReactNode;
}

const hashToName = (h: string): string => {
  // Hash is path-based: -Users-username-Documents-GitHub-project-name
  // Strip common path prefixes to recover the project directory name
  const prefixes = [
    /^-(?:Users|home)-[^-]+-Documents-GitHub-/,
    /^-(?:Users|home)-[^-]+-Documents-/,
    /^-(?:Users|home)-[^-]+-/,
  ];
  for (const re of prefixes) {
    const m = h.match(re);
    if (m && m[0].length < h.length) return h.slice(m[0].length);
  }
  return h;
};

const projectDisplayName = (p: { project_hash: string; project_name: string; last_session_at?: string | null }): string => {
  const name = p.project_name || hashToName(p.project_hash);
  if (!p.last_session_at) return name;
  return `${name} · ${formatRelativeTime(p.last_session_at).toLowerCase()}`;
};

const btnActive = 'px-3 py-1.5 text-[13px] font-medium transition-colors bg-brand text-brand-ink';
const btnInactive = 'px-3 py-1.5 text-[13px] font-medium transition-colors bg-surface text-ink-2 hover:bg-surface-sunk';
const toggleGroup = 'flex rounded-lg overflow-hidden border border-border';

const FilterBar = ({
  viewMode,
  onViewModeChange,
  showViewToggle = true,
  modelCategory,
  onModelCategoryChange,
  showModelCategory = true,
  groupBy,
  onGroupByChange,
  showGroupBy = true,
  onRefresh,
  showRefresh = true,
  search,
  onSearchChange,
  searchPlaceholder = 'Search...',
  showSearch = false,
  projects,
  onProjectDelete,
  nowrap = false,
  selectedProject,
  onProjectChange,
  showProjectFilter = false,
  modelFilter,
  onModelFilterChange,
  models,
  showModelFilter = false,
  userFilter,
  onUserFilterChange,
  users,
  showUserFilter = false,
  nameMap = {},
  children,
}: FilterBarProps) => {
  const handleSearch = (e: React.ChangeEvent<HTMLInputElement>) => onSearchChange?.(e.target.value);
  // 동일 project_hash가 데이터에 중복으로 들어올 수 있어, 옵션을 hash 기준 1개만 렌더(중복 key 방지).
  const uniqueProjects = (projects ?? []).filter(
    (p, i, arr) => arr.findIndex((x) => x.project_hash === p.project_hash) === i,
  );
  const selectedLabel = selectedProject
    ? (() => {
        const hit = uniqueProjects.find((p) => p.project_hash === selectedProject);
        return hit ? projectDisplayName(hit) : undefined;
      })()
    : undefined;

  return (
    // Children shrink to half their basis before the row is allowed to wrap, so a
    // narrow window loses width rather than gaining a line.
    // nowrap does NOT add its own scroller. Measured at 780px this container had 76px
    // of width holding 234px of content and was scrolling inside a parent that was
    // also scrolling -- two nested scroll areas, the inner one useless at that size
    // and the outer one left with nothing to move. The caller owns the scrolling; this
    // just refuses to wrap and lets its children shrink.
    <div className={cn('flex min-w-0 flex-1 items-center gap-3 max-lg:[&_button]:min-h-11',
      nowrap ? 'flex-nowrap' : 'flex-wrap')}>
      {showViewToggle && onViewModeChange && (
        <div className={toggleGroup}>
          <button
            onClick={() => onViewModeChange('cost')}
            className={viewMode === 'cost' ? btnActive : btnInactive}
          >
            Cost
          </button>
          <button
            onClick={() => onViewModeChange('token')}
            className={viewMode === 'token' ? btnActive : btnInactive}
          >
            Token
          </button>
        </div>
      )}

      {showModelCategory && onModelCategoryChange && (
        <div className={toggleGroup}>
          <button
            onClick={() => onModelCategoryChange('all')}
            className={modelCategory === 'all' ? btnActive : btnInactive}
          >
            All
          </button>
          <button
            onClick={() => onModelCategoryChange('anthropic')}
            className={modelCategory === 'anthropic' ? btnActive : btnInactive}
          >
            Claude
          </button>
          <button
            onClick={() => onModelCategoryChange('codex')}
            className={modelCategory === 'codex' ? btnActive : btnInactive}
          >
            Codex
          </button>
          <button
            onClick={() => onModelCategoryChange('compatible')}
            className={modelCategory === 'compatible' ? btnActive : btnInactive}
            title="Anthropic·OpenAI 외 모든 billing provider — Amazon Bedrock, agent-memory, 서드파티 OpenAI 호환 엔드포인트 등"
          >
            Compatible
          </button>
        </div>
      )}

      {showGroupBy && onGroupByChange && (
        <div className={toggleGroup}>
          <button
            onClick={() => onGroupByChange('model')}
            className={groupBy === 'model' ? btnActive : btnInactive}
          >
            by Model
          </button>
          <button
            onClick={() => onGroupByChange('user')}
            className={groupBy === 'user' ? btnActive : btnInactive}
          >
            by User
          </button>
        </div>
      )}

      {showModelFilter && onModelFilterChange && (
        <Dropdown
          value={modelFilter ?? '__all__'}
          onChange={onModelFilterChange}
          placeholder="All Models"
          options={[
            { value: '__all__', label: 'All Models' },
            ...(models ?? []).map((m) => ({ value: m, label: modelLabel(m) })),
          ]}
        />
      )}

      {showUserFilter && groupBy === 'user' && onUserFilterChange && (
        <Dropdown
          value={userFilter ?? '__all__'}
          onChange={onUserFilterChange}
          placeholder="All Users"
          mono
          options={[
            { value: '__all__', label: 'All Users' },
            ...(users ?? []).map((u) => ({ value: u, label: nameMap[u] || u.slice(0, 8) })),
          ]}
        />
      )}

      {showRefresh && onRefresh && (
        <Button
          variant="outline"
          size="icon"
          onClick={onRefresh}
          aria-label="Refresh data"
          className="px-3 py-1.5 rounded-lg border-border bg-surface text-ink-2 hover:bg-surface-sunk"
        >
          <RefreshCw size={18} />
        </Button>
      )}

      {showProjectFilter && onProjectChange && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              className="flex min-w-[110px] max-w-[420px] flex-1 shrink basis-[220px] items-center justify-between gap-2 rounded-lg border border-border bg-surface px-3 py-1.5 text-[13px] text-ink-2 focus:border-brand focus:outline-none"
            >
              <span className="truncate">
                {selectedLabel ?? 'All Projects'}
              </span>
              <ChevronDown size={14} className="shrink-0 text-ink-3" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            align="start"
            className="max-h-[60vh] w-[calc(100vw-2rem)] min-w-0 max-w-[calc(100vw-2rem)] overflow-y-auto sm:w-[var(--radix-dropdown-menu-trigger-width)]"
          >
            <DropdownMenuItem onSelect={() => onProjectChange('')}>
              <span className="truncate">All Projects</span>
            </DropdownMenuItem>
            {uniqueProjects.map((p) => (
              // The delete control is a SIBLING of the menu item, not a child of
              // it. Nested, it never fired: Radix decides a menu item is chosen on
              // pointerup, from a listener bound to the item's own DOM node, so a
              // child's onPointerDown guard is too early and its onClick guard --
              // a React synthetic handler -- is too late. Clicking the icon
              // selected the project and closed the menu, leaving no way to reach
              // deletion at all (#432).
              <div
                key={p.project_hash}
                className="group flex items-center gap-1 pr-1"
              >
                <DropdownMenuItem
                  onSelect={() => onProjectChange(p.project_hash)}
                  className="min-w-0 flex-1"
                >
                  <span className="truncate">{projectDisplayName(p)}</span>
                </DropdownMenuItem>
                {onProjectDelete && (
                  <button
                    type="button"
                    aria-label={`${p.project_name || p.project_hash} 삭제`}
                    title="프로젝트 삭제"
                    onClick={() => onProjectDelete(p)}
                    className="min-h-11 min-w-11 shrink-0 rounded p-1 text-ink-3 opacity-0 transition-opacity hover:text-danger focus-visible:opacity-100 group-hover:opacity-100 lg:min-h-0 lg:min-w-0"
                  >
                    <Trash2 size={13} />
                  </button>
                )}
              </div>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      )}

      {showSearch && onSearchChange && (
        <Input
          type="text"
          placeholder={searchPlaceholder}
          value={search ?? ''}
          onChange={handleSearch}
          // Shrinks instead of wrapping: basis is the width it wants, min-w is half of
          // that, and flex-shrink does the rest. A wrapped toolbar costs a whole row of
          // vertical space for a control that only needed to be narrower.
          className="h-auto min-h-11 w-56 min-w-28 flex-1 shrink basis-56 rounded-md border-border px-3 py-1.5 text-[13px] shadow-none focus-visible:border-brand focus-visible:ring-2 focus-visible:ring-brand/30 lg:min-h-0"
        />
      )}

      {children}
    </div>
  );
}

export { FilterBar };
