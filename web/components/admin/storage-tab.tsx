'use client';

import type { CSSProperties } from 'react';
import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { fetchStorageReport } from '@/lib/api';
import { POLL_SLOW } from '@/lib/query-config';
import { cn } from '@/lib/utils';
import { formatBytes, formatRetentionDays, formatCompressionDays } from '@/lib/format';
import { storageViewState } from '@/lib/storage-view';
import { CollectingLoader } from '@/components/common/collecting-loader';
import { RetentionEditDialog } from '@/components/storage/retention-edit-dialog';
import { useAuth } from '@/components/common/auth-context';
import type { RetentionAxis, StorageReport } from '@/lib/types';

// minmax floor on the first column + a min-width wrapper keeps columns from
// merging on narrow viewports; the wrapper scrolls horizontally instead
// (DESIGN.md: 데이터 테이블은 가로 스크롤 허용, 줄바꿈 금지).
const GRID_CLASS = 'grid grid-cols-[minmax(160px,1.5fr)_110px_120px_130px_120px_70px] items-center';

// Retention is set per axis (OTEL drives both otel tables); map each row to its axis.
const AXIS_OF: Record<string, RetentionAxis> = {
  otel_events: 'otel',
  otel_metrics: 'otel',
  session_records: 'session',
};
const AXIS_LABEL: Record<RetentionAxis, string> = {
  otel: 'otel_events + otel_metrics',
  session: 'session_records',
};

const VolumePanel = ({ volume }: { volume: StorageReport['volume'] }) => {
  if (!volume || !Number.isFinite(volume.total_bytes) || volume.total_bytes <= 0) {
    return (
      <div className="bg-surface border border-border rounded-lg px-4 py-4 text-sm text-ink-3">
        Volume usage unavailable.
      </div>
    );
  }
  const used = Number.isFinite(volume.used_bytes) ? volume.used_bytes : 0;
  const usedPct = Math.min(100, Math.max(0, (used / volume.total_bytes) * 100));
  const danger = usedPct >= 85;
  return (
    <div className="bg-surface border border-border rounded-lg px-4 py-4 space-y-2">
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-[13px] font-medium text-ink shrink-0">Data Volume</span>
        <span className="text-[12px] text-ink-3 font-mono truncate">{volume.path}</span>
      </div>
      <div className="h-2.5 w-full rounded-full bg-surface-sunk overflow-hidden">
        <div
          className={cn('h-full rounded-full transition-all w-[var(--bar-pct)]', danger ? 'bg-danger' : 'bg-brand')}
          style={{ '--bar-pct': `${usedPct}%` } as CSSProperties} // eslint-disable-line no-restricted-syntax
        />
      </div>
      <div className="flex items-center justify-between text-[12px]">
        <span className={cn('font-medium', danger ? 'text-danger' : 'text-ink-2')}>
          {formatBytes(volume.free_bytes)} free
        </span>
        <span className="text-ink-3">
          {formatBytes(volume.used_bytes)} / {formatBytes(volume.total_bytes)} ({usedPct.toFixed(0)}%)
        </span>
      </div>
    </div>
  );
};

const StorageTab = () => {
  const { isAdmin } = useAuth();
  const queryClient = useQueryClient();
  const [edit, setEdit] = useState<{ axis: RetentionAxis; currentDays: number | null } | null>(null);
  const { data, isLoading, isError } = useQuery<StorageReport>({
    queryKey: ['storage-report'],
    queryFn: fetchStorageReport,
    enabled: isAdmin,
    refetchInterval: POLL_SLOW,
  });

  if (!isAdmin) {
    return <div className="px-4 py-6 text-sm text-ink-3">관리자 전용 페이지입니다.</div>;
  }

  const tables = data?.tables ?? [];
  const view = storageViewState({ isLoading, isError, tableCount: tables.length });

  const handleEditClick = (axis: RetentionAxis, currentDays: number | null) => setEdit({ axis, currentDays });
  const handleEditSuccess = () => {
    queryClient.invalidateQueries({ queryKey: ['storage-report'] });
    setEdit(null);
  };
  const handleEditCancel = () => setEdit(null);

  return (
    <div className="space-y-4">
      <header>
        <h2 className="text-[16px] font-semibold text-ink">Storage</h2>
        <p className="text-[13px] text-ink-3 mt-0.5">Retention, table sizes, and volume free space</p>
      </header>

      {view === 'error' ? (
        <div className="bg-danger-soft border border-danger/40 rounded-lg px-4 py-6 text-sm text-danger-strong">
          Failed to load storage info. Please try again.
        </div>
      ) : (
        <>
          <VolumePanel volume={data?.volume ?? null} />

          <div className="bg-surface border border-border rounded-lg overflow-x-auto">
            <div className="min-w-[710px]">
              <header className={cn(GRID_CLASS, 'px-4 py-2.5 border-b border-surface-sunk bg-canvas')}>
                <span className="text-[11px] font-medium text-ink-3">Table</span>
                <span className="text-[11px] font-medium text-ink-3">Retention</span>
                <span className="text-[11px] font-medium text-ink-3">Compression</span>
                <span className="text-[11px] font-medium text-ink-3 text-right">Rows (approx)</span>
                <span className="text-[11px] font-medium text-ink-3 text-right">Size</span>
                <span className="text-[11px] font-medium text-ink-3 text-right">Edit</span>
              </header>

              {view === 'loading' && <CollectingLoader className="py-6" />}

              {view === 'empty' && (
                <div className="px-4 py-6 text-center text-sm text-ink-3">No table data</div>
              )}

              {tables.map((t) => (
                <div
                  key={t.table}
                  className={cn(GRID_CLASS, 'px-4 py-3 border-b border-surface-sunk last:border-b-0 hover:bg-canvas transition-colors')}
                >
                  <span className="text-[13px] text-ink font-mono truncate pr-2">{t.table}</span>
                  <span className="text-[12px] text-ink-2">{formatRetentionDays(t.retention_days)}</span>
                  <span className="text-[12px] text-ink-2">{formatCompressionDays(t.compression_days)}</span>
                  <span className="text-[12px] text-ink-2 text-right tabular-nums">{t.rows_approx.toLocaleString()}</span>
                  <span className="text-[12px] text-ink text-right tabular-nums">{formatBytes(t.size_bytes)}</span>
                  <span className="text-right">
                    {data?.env_managed?.[AXIS_OF[t.table]] ? (
                      <span
                        className="text-[10px] font-medium text-ink-3"
                        title="Pinned by an env var — editing disabled (boot would override)"
                      >
                        env
                      </span>
                    ) : (
                      <button
                        type="button"
                        onClick={() => handleEditClick(AXIS_OF[t.table], t.retention_days)}
                        className="text-[11px] font-medium text-brand hover:underline"
                      >
                        Edit
                      </button>
                    )}
                  </span>
                </div>
              ))}
            </div>
          </div>
        </>
      )}

      {edit && (
        <RetentionEditDialog
          key={edit.axis}
          axis={edit.axis}
          axisLabel={AXIS_LABEL[edit.axis]}
          currentDays={edit.currentDays}
          onSuccess={handleEditSuccess}
          onCancel={handleEditCancel}
        />
      )}
    </div>
  );
};

export { StorageTab };
