'use client';

import { useQuery } from '@tanstack/react-query';
import { fetchAppVersionInfo, fetchClientVersions } from '@/lib/api';
import { POLL_SLOW } from '@/lib/query-config';
import { cn } from '@/lib/utils';
import { classifyClientVersion, sortClientsByLag, summarizeClientVersions } from '@/lib/client-version';
import type { ClientVersionStatus } from '@/lib/client-version';
import { CollectingLoader } from '@/components/common/collecting-loader';
import { useAuth } from '@/components/common/auth-context';
import type { ClientVersionInfo } from '@/lib/types';

// The identity columns -- Profile Email, Name, User ID -- flex; the three on the
// right are fixed. Profile Email and User ID were once both 1fr and absorbed every
// spare pixel, leaving the fixed columns too narrow: the status badge wrapped onto
// two lines and a long client version ran into Last Seen. Identity content is short
// and truncatable, so it gives up the room; a wrapped "outdated (server vX)" reads
// as two rows and cannot.
const GRID_CLASS =
  'grid grid-cols-[minmax(160px,1fr)_minmax(110px,0.6fr)_minmax(96px,0.5fr)_180px_200px_250px] items-center';

const STATUS_LABEL: Record<ClientVersionStatus, string> = {
  behind: 'outdated',
  current: 'current',
  // Same release tag, a build the server is not on (an older one, or a sibling
  // branch at the same commit count). The client's updater compares base tags
  // only, so it will never pull this one forward — green, not red, because there
  // is nothing an admin can chase. Not labelled "current": the version string in
  // the next column visibly differs from the server's, and claiming the two match
  // is what gets a bug filed.
  'build-differs': 'other build',
  ahead: 'ahead',
  unknown: 'unknown',
  unreported: 'unreported',
};

const STATUS_CLASS: Record<ClientVersionStatus, string> = {
  behind: 'bg-danger-soft text-danger-strong',
  current: 'bg-success-soft text-success-strong',
  'build-differs': 'bg-success-soft text-success-strong',
  ahead: 'bg-success-soft text-success-strong',
  unknown: 'bg-surface-sunk text-ink-3',
  unreported: 'bg-danger-soft text-danger-strong',
};

const StatusBadge = ({
  status,
  serverVersion,
}: {
  status: ClientVersionStatus;
  serverVersion: string | undefined;
}) => {
  let label: string = STATUS_LABEL[status];
  if (status === 'behind' && serverVersion) label = `outdated (server ${serverVersion})`;
  // Not a `< <version>` floor: an empty version means the request carried no
  // X-Cctrace-Version header, and a build without the version ldflag omits it
  // just as an old client does. The floor is not established (#455).
  if (status === 'unreported') label = 'version unreported';
  return (
    <span className={cn('inline-block whitespace-nowrap rounded px-2 py-0.5 text-[11px] font-medium', STATUS_CLASS[status])}>
      {label}
    </span>
  );
};

const ClientsTab = () => {
  const { isAdmin } = useAuth();
  const { data: versions = [], isLoading } = useQuery<ClientVersionInfo[]>({
    queryKey: ['client-versions'],
    queryFn: fetchClientVersions,
    enabled: isAdmin,
    refetchInterval: POLL_SLOW,
  });
  // Static lookup (staleTime: Infinity) — same query the sidebar already uses,
  // so this never issues a second /api/version call.
  const { data: appVersionInfo } = useQuery({
    queryKey: ['app-version-info'],
    queryFn: fetchAppVersionInfo,
    staleTime: Infinity,
  });

  if (!isAdmin) {
    return <div className="px-4 py-6 text-sm text-ink-3">관리자 전용 페이지입니다.</div>;
  }

  const serverVersion = appVersionInfo?.version;
  const sorted = sortClientsByLag(versions, serverVersion, (v) => v.last_seen_at);
  const summary = summarizeClientVersions(sorted, serverVersion);

  return (
    <div className="space-y-4">
      <header className="flex items-center justify-between">
        <h2 className="text-[16px] font-semibold text-ink">Clients</h2>
        <span className="text-[13px] text-ink-2">{summary.text}</span>
      </header>

      <div className="bg-surface border border-border rounded-lg overflow-visible">
        <header className={cn(GRID_CLASS, 'px-4 py-2.5 border-b border-surface-sunk bg-canvas')}>
          <span className="text-[11px] font-medium text-ink-3">Profile Email</span>
          <span className="text-[11px] font-medium text-ink-3">Name</span>
          <span className="text-[11px] font-medium text-ink-3">User ID</span>
          <span className="text-[11px] font-medium text-ink-3">Client Version</span>
          <span className="text-[11px] font-medium text-ink-3">Last Seen</span>
          <span className="text-[11px] font-medium text-ink-3">Status</span>
        </header>

        {isLoading && <CollectingLoader className="py-6" />}

        {!isLoading && sorted.length === 0 && (
          <div className="px-4 py-6 text-center text-sm text-ink-3">No client versions reported yet</div>
        )}

        {sorted.map((v) => {
          const status = classifyClientVersion(v.client_version, serverVersion);
          return (
            <div
              key={v.profile_email}
              className={cn(GRID_CLASS, 'px-4 py-3 border-b border-surface-sunk last:border-b-0 hover:bg-canvas transition-colors')}
            >
              <span className="text-[13px] text-ink truncate pr-2">{v.profile_email}</span>
              <span className="text-[13px] text-ink truncate pr-2">{v.name || '—'}</span>
              <span className="text-[12px] text-ink-2 font-mono truncate pr-2">{v.user_id || '—'}</span>
              <div className="min-w-0 pr-2 font-mono">
                <div className="truncate whitespace-nowrap text-[12px] text-ink">
                  {v.client_version || '(미보고)'}
                </div>
                <div className="truncate whitespace-nowrap text-[11px] text-ink-3">
                  {v.client_os && v.client_arch ? `${v.client_os} · ${v.client_arch}` : 'Platform unknown'}
                </div>
                {/* One binary replaced on disk while an old resident process kept
                    running writes both versions at once, and the badge beside it
                    shows only the newest -- so the account reads as up to date
                    while half its records come from the stale one (#623). */}
                {(v.concurrent_versions ?? 0) > 1 && (
                  <div className="truncate whitespace-nowrap text-[11px] text-warning-strong">
                    최근 24시간에 버전 {v.concurrent_versions}개 동시 보고
                  </div>
                )}
                {/* 갱신이 막힌 설치는 스스로 낫지 않는다. 횟수만으로는 무엇을
                    고쳐야 할지 알 수 없어 목표 버전·시작 시각·클라이언트가 낸
                    오류 문구를 함께 적는다 (#750). */}
                {(v.update_fail_count ?? 0) > 0 && (
                  <div className="text-[11px] text-danger-strong">
                    <div className="truncate whitespace-nowrap">
                      {v.update_target_version || '새 버전'} 갱신 실패 {v.update_fail_count}회
                      {v.update_first_failed_at && ` · ${new Date(v.update_first_failed_at).toLocaleDateString()}부터`}
                    </div>
                    {v.update_fail_reason && <div className="whitespace-normal break-words">{v.update_fail_reason}</div>}
                  </div>
                )}
                {/* 보고할 줄 모르는 구버전을 "이상 없음"으로 읽지 않는다. 침묵과
                    정상 보고는 다른 사실이고, 한쪽만 안전하다. */}
                {v.update_reported === false && (
                  <div className="truncate whitespace-nowrap text-[11px] text-ink-3">갱신 상태 미보고</div>
                )}
              </div>
              <span className="whitespace-nowrap pr-2 text-[12px] text-ink-3">{new Date(v.last_seen_at).toLocaleString()}</span>
              <span>
                <StatusBadge status={status} serverVersion={serverVersion} />
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
};

export { ClientsTab };
