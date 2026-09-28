'use client';

import type { CSSProperties, DragEvent } from 'react';
import { ChevronRight } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { CostSummary } from '@/lib/types';
import type { ViewMode } from '@/components/common/trend-chart';
import { fmt, resolveDisplayName } from './user-utils';

interface UserCardStats {
  total_cost: number;
  total_input_tokens: number;
  total_output_tokens: number;
  request_count: number;
}

interface UserCardProps {
  user: CostSummary;
  avatarColor: string;
  dashName?: string;
  stats: UserCardStats;
  viewMode: ViewMode;
  isAdmin: boolean;
  isSelected: boolean;
  isDragging: boolean;
  isDropTarget: boolean;
  isHovered: boolean;
  /** True when another card is being dragged and this one may accept the drop. */
  canAcceptDrop: boolean;
  onToggleSelect: () => void;
  onHoverChange: (hovered: boolean) => void;
  onDragStart: () => void;
  onDragEnd: () => void;
  onDragEnter: () => void;
  onDragLeave: () => void;
  onDrop: () => void;
  onDelete: () => void;
}

const UserCard = ({
  user,
  avatarColor,
  dashName,
  stats,
  viewMode,
  isAdmin,
  isSelected,
  isDragging,
  isDropTarget,
  isHovered,
  canAcceptDrop,
  onToggleSelect,
  onHoverChange,
  onDragStart,
  onDragEnd,
  onDragEnter,
  onDragLeave,
  onDrop,
  onDelete,
}: UserCardProps) => {
  const { shortName, unconfigured } = resolveDisplayName(user);

  const handleDragStart = (e: DragEvent<HTMLDivElement>) => {
    onDragStart();
    e.dataTransfer.effectAllowed = 'move';
  };
  const handleDragOver = (e: DragEvent<HTMLDivElement>) => {
    // Only react when another card is being dragged onto this one.
    if (!canAcceptDrop) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    onDragEnter();
  };
  const handleDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    if (!canAcceptDrop) return;
    onDrop();
  };
  const handleMouseEnter = () => onHoverChange(true);
  const handleMouseLeave = () => onHoverChange(false);
  const handleDelete = (e: React.MouseEvent<HTMLButtonElement>) => {
    e.stopPropagation();
    onDelete();
  };

  return (
    <div
      draggable={isAdmin}
      onDragStart={isAdmin ? handleDragStart : undefined}
      onDragEnd={isAdmin ? onDragEnd : undefined}
      onDragOver={isAdmin ? handleDragOver : undefined}
      onDragLeave={isAdmin ? onDragLeave : undefined}
      onDrop={isAdmin ? handleDrop : undefined}
      onMouseEnter={handleMouseEnter}
      onMouseLeave={handleMouseLeave}
      className={cn(
        'rounded-lg border transition-all duration-150 relative',
        isSelected
          ? 'bg-surface-sunk border-brand border-[1.5px]'
          : isDropTarget
            ? 'bg-surface border-brand ring-2 ring-brand/20 scale-[1.01]'
            : 'bg-surface border-border',
        isDragging && 'opacity-40',
      )}
    >
      <button
        onClick={onToggleSelect}
        className="w-full px-5 py-4 flex items-center gap-4 hover:bg-canvas/50 transition-colors"
      >
        <span
          className="w-10 h-10 rounded-full flex items-center justify-center text-white text-[16px] font-semibold shrink-0 [background-color:var(--avatar-color)]"
          // CSS variable drives the runtime avatar color (Tailwind cannot express dynamic values)
          style={{ '--avatar-color': avatarColor } as CSSProperties} // eslint-disable-line no-restricted-syntax
        >
          {(shortName[0] || '?').toUpperCase()}
        </span>

        <div className="w-[180px] shrink-0 text-left">
          <div className="text-[14px] font-semibold text-ink truncate">
            {dashName ? (
              <>
                <span>{dashName}</span> <span className="text-ink-3 text-xs font-normal">({shortName})</span>
              </>
            ) : (
              shortName
            )}
          </div>
          {user.profile_email && <div className="text-[11px] text-ink-2 truncate">{user.profile_email}</div>}
          {(user.login_emails ?? []).map((le) => (
            <div key={le} className="text-[10px] text-ink-3 truncate">{le}</div>
          ))}
          {unconfigured && !user.profile_email && (
            <span className="text-[10px] font-medium px-1.5 py-0.5 rounded bg-warning-soft text-warning-strong border border-warning/40">
              미설정
            </span>
          )}
        </div>

        <div className="flex-1 flex items-center gap-6">
          <div className="flex flex-col items-center gap-0.5">
            <span className="text-[16px] font-semibold text-ink">
              {viewMode === 'token' ? fmt(stats.total_input_tokens) : fmt(stats.total_input_tokens + stats.total_output_tokens)}
            </span>
            <span className="text-[11px] text-ink-3">{viewMode === 'token' ? 'Input' : 'Tokens'}</span>
          </div>
          <div className="flex flex-col items-center gap-0.5">
            <span className="text-[16px] font-semibold text-ink">
              {viewMode === 'token' ? fmt(stats.total_output_tokens) : String(stats.request_count)}
            </span>
            <span className="text-[11px] text-ink-3">{viewMode === 'token' ? 'Output' : 'Requests'}</span>
          </div>
          <div className="flex flex-col items-center gap-0.5">
            <span className="text-[16px] font-semibold text-ink">
              {viewMode === 'token' ? fmt(stats.total_input_tokens + stats.total_output_tokens) : `$${stats.total_cost.toFixed(4)}`}
            </span>
            <span className="text-[11px] text-ink-3">{viewMode === 'token' ? 'Tokens' : 'Cost'}</span>
          </div>
        </div>

        <ChevronRight size={18} className="text-ink-3 shrink-0" />
      </button>

      {isAdmin && isHovered && !isSelected && (
        <button
          onClick={handleDelete}
          title="삭제"
          className="absolute right-10 top-1/2 -translate-y-1/2 w-6 h-6 flex items-center justify-center rounded-full text-ink-3 hover:bg-danger-soft hover:text-danger transition-colors text-xs font-bold"
        >
          x
        </button>
      )}
    </div>
  );
};

export { UserCard };
export type { UserCardProps, UserCardStats };
