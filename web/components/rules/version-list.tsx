import { cn } from '@/lib/utils';
import { formatDateTime } from './rule-helpers';
import type { ProjectRuleVersion } from '@/lib/types';

interface VersionListProps {
  versions: ProjectRuleVersion[];
  selectedVersionId: number;
  currentVersionId?: number;
  onSelectVersion: (versionId: number) => void;
}

const VersionList = ({
  versions,
  selectedVersionId,
  currentVersionId,
  onSelectVersion,
}: VersionListProps) => {
  return (
    <div className="border-b border-border px-4 py-3">
      <div className="text-[12px] font-medium uppercase text-ink-3">Versions</div>
      <div className="mt-2 space-y-1">
        {versions.map(version => {
          const selected = selectedVersionId === version.id;
          const current = currentVersionId === version.id;
          const latest = versions[0]?.id === version.id;
          return (
            <button
              key={version.id}
              onClick={() => onSelectVersion(version.id)}
              className={cn(
                'w-full rounded-md border px-3 py-2 text-left transition-colors',
                selected
                  ? 'border-brand bg-surface'
                  : 'border-border bg-canvas hover:bg-surface'
              )}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="text-[12px] font-medium text-ink">v{version.version_number}</span>
                <span className="flex items-center gap-1">
                  {latest && <span className="text-[10px] font-medium text-ink-2">latest</span>}
                  {current && <span className="text-[10px] font-medium text-brand">current</span>}
                </span>
              </div>
              <div className="mt-1 text-[11px] text-ink-3">{formatDateTime(version.discovered_at)}</div>
            </button>
          );
        })}
      </div>
    </div>
  );
};

export { VersionList };
