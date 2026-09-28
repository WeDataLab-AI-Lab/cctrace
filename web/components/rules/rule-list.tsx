import { GitBranch } from 'lucide-react';
import { cn } from '@/lib/utils';
import { CollectingLoader } from '@/components/common/collecting-loader';
import { StatusBadge } from './status-badge';
import { agentLabel } from './rule-helpers';
import type { RuleRow } from './rule-helpers';

interface RuleGroup {
  key: string;
  name: string;
  rows: RuleRow[];
}

interface RuleListProps {
  groups: RuleGroup[];
  selectedKey: string | null;
  isLoading: boolean;
  emptyMessage: string;
  onSelectRule: (key: string) => void;
}

const RuleList = ({ groups, selectedKey, isLoading, emptyMessage, onSelectRule }: RuleListProps) => {
  if (isLoading) {
    return <CollectingLoader className="py-8" />;
  }

  if (groups.length === 0) {
    return <div className="px-4 py-8 text-center text-sm text-ink-3">{emptyMessage}</div>;
  }

  return (
    <div className="divide-y divide-surface-sunk">
      {groups.map(group => (
        <div key={group.key} className="px-3 py-3">
          <div className="mb-2 flex items-center gap-2 px-1">
            <GitBranch size={13} className="text-ink-3" />
            <div className="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">{group.name}</div>
            <span className="text-[11px] text-ink-3">{group.rows.length}</span>
          </div>
          <div className="space-y-1">
            {group.rows.map(row => {
              const selected = selectedKey === row.key;
              return (
                <button
                  key={row.key}
                  onClick={() => onSelectRule(row.key)}
                  className={cn(
                    'w-full rounded-md px-3 py-2 text-left transition-colors',
                    selected ? 'bg-surface-sunk text-ink' : 'text-ink-2 hover:bg-canvas'
                  )}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="min-w-0 truncate font-mono text-[12px]">{row.rule.rule_path}</span>
                    <StatusBadge status={row.rule.current_status} />
                  </div>
                  <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-ink-3">
                    <span>{agentLabel(row.rule.agent)}</span>
                    <span>/</span>
                    <span>{row.rule.rule_scope}</span>
                    <span>/</span>
                    <span>{row.rule.version_count} versions</span>
                  </div>
                </button>
              );
            })}
          </div>
        </div>
      ))}
    </div>
  );
};

export { RuleList };
export type { RuleGroup };
