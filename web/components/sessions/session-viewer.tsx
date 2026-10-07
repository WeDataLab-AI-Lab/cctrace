'use client';

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Trash2, Settings } from 'lucide-react';
import { fetchAppVersionInfo } from '@/lib/api';
import { formatCctraceVersion, formatClaudeCodeVersion } from '@/lib/version-display';
import { agentLabel } from '@/lib/agent-label';
import type { SessionOverview } from '@/lib/types';
import { needsAccountNotice, projectLabel } from './session-utils';
import { SessionDetail } from './session-detail';
import { AccountSegments } from './account-segments';
import { DeleteSessionDialog } from './delete-session-dialog';
import { BlockedProjectsDialog } from './blocked-projects-dialog';
import { useAuth } from '@/components/common/auth-context';
import { fetchDeletionPolicy } from '@/lib/api';
import type { ViewMode } from './mode-toggle';

interface SessionViewerProps {
  session: SessionOverview | null;
  mode: ViewMode;
  onNavigate: (sessionId: string, anchorUuid?: string) => void;
  scrollAnchor?: string;
  scrollFromTs?: string;
  /** Called after a delete so the list can drop the row and clear the selection. */
  onDeleted?: (sessionId: string) => void;
}

// claude_version 은 Claude Code 전용 이름이지만 Codex 세션도 같은 컬럼에 버전이 실릴 수 있다.
// 서버는 agent 가 비면 'claude' 로 읽으므로(COALESCE) 여기서도 같게 읽는다. Claude 만 버전이
// 비었을 때 '—' 를 보이고, 그 외 에이전트는 모르는 버전을 '—' 로 채우지 않고 생략한다.
const harnessLabel = (session: SessionOverview): string => {
  const agent = session.agent || 'claude';
  if (agent === 'claude') return `${agentLabel(agent)} ${formatClaudeCodeVersion(session.claude_version)}`;
  return session.claude_version ? `${agentLabel(agent)} ${session.claude_version}` : agentLabel(agent);
};

const SessionViewer = ({ session, mode, onNavigate, scrollAnchor, scrollFromTs, onDeleted }: SessionViewerProps) => {
  const { isAdmin } = useAuth();
  const qc = useQueryClient();
  const [showDelete, setShowDelete] = useState(false);
  const [showSettings, setShowSettings] = useState(false);

  // Read by everyone, not just admins: the dashboard needs it to decide whether to
  // draw the button at all, and hiding the answer would only mean drawing a button
  // that 403s.
  const { data: policy } = useQuery({
    queryKey: ['deletion-policy'],
    queryFn: fetchDeletionPolicy,
  });
  // Ownership is settled server-side. Showing the button to a non-admin whenever
  // the policy allows owner deletes keeps this component from re-deriving an access
  // rule that already exists in one place -- at worst the server answers 403.
  const canDelete = isAdmin || !!policy?.allow_owner_delete;
  // Static lookup (staleTime: Infinity) shared with the sidebar and Clients tab's
  // queryKey — session_record_version_since is the confirmed lower bound below
  // which cctrace_version is unrecorded (see version-display.ts).
  const { data: appVersionInfo } = useQuery({
    queryKey: ['app-version-info'],
    queryFn: fetchAppVersionInfo,
    staleTime: Infinity,
  });

  if (!session) {
    return (
      <div className="flex-1 flex flex-col overflow-hidden">
        <div className="flex-1 flex items-center justify-center text-sm text-ink-3">
          Select a session to view
        </div>
      </div>
    );
  }

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      {/* Identity bar.
          Everything here used to be a summary: address, project, user id, login,
          In/Out/Cost. All five are on the left list item that is highlighted while
          this pane is open, so the pane spent a block of vertical space restating
          what was already on screen -- the first thing to give on a 13" laptop.
          What is left is what the list cannot say: the full session id, the client
          versions that a version-stuck report is read off (#623), and the two
          actions. The account caveats join them only when they apply. */}
      <div className="flex-shrink-0 flex items-center gap-3 px-5 py-2 border-b border-border">
        <span className="font-mono text-xs text-ink-2 truncate" title={session.session_id ?? '-'}>
          {session.session_id ?? '-'}
        </span>
        {appVersionInfo && (
          <span className="hidden shrink-0 text-[11px] text-ink-3 sm:inline">
            cctrace {formatCctraceVersion(session.cctrace_version, appVersionInfo.session_record_version_since)}
            {' · '}
            {harnessLabel(session)}
          </span>
        )}
        {needsAccountNotice(session) && (
          <span className="flex shrink-0 items-center gap-1.5 text-[11px] text-ink-3">
            {session.login_email_inferred && (
              <span
                title="관측된 값이 아닙니다. 이 세션은 계정을 알려주는 로그를 남기지 않아, 같은 사용자의 앞뒤 로그인 기록으로 추정한 계정입니다."
                className="inline-flex items-center rounded bg-warning-soft px-1.5 py-0.5 text-[10px] font-medium text-warning-strong"
              >
                inferred
              </span>
            )}
            {(session.account_count ?? 1) > 1 && (
              <span title={`이 세션은 ${session.account_count}개 계정에 걸쳐 있습니다. 표시된 계정은 대표값입니다.`}>
                대표 · {session.account_count}개 계정
              </span>
            )}
          </span>
        )}
        <span className="ml-auto flex shrink-0 items-center gap-1">
          {canDelete && session.session_id && (
            <button
              type="button"
              aria-label="세션 삭제"
              title="세션 삭제"
              onClick={() => setShowDelete(true)}
              className="rounded p-1 text-ink-3 hover:bg-surface-sunk hover:text-danger"
            >
              <Trash2 className="size-3.5" />
            </button>
          )}
          {isAdmin && (
            <button
              type="button"
              aria-label="세션 수집 설정"
              title="세션 수집 설정"
              onClick={() => setShowSettings(true)}
              className="rounded p-1 text-ink-3 hover:bg-surface-sunk hover:text-ink"
            >
              <Settings className="size-3.5" />
            </button>
          )}
        </span>
      </div>
      {needsAccountNotice(session) && (
        <AccountSegments sessionId={session.session_id} accountCount={session.account_count} />
      )}
      {/* Conversation */}
      <div className="flex-1 flex flex-col min-h-0 overflow-hidden">
        <SessionDetail key={`${session.session_id}:${mode}`} sessionId={session.session_id} mode={mode} onNavigate={onNavigate} scrollAnchor={scrollAnchor} scrollFromTs={scrollFromTs} hasEnriched={session.has_enriched} />
      {showDelete && session.session_id && (
        <DeleteSessionDialog
          sessionId={session.session_id}
          projectHash={session.project_hash || ''}
          projectName={projectLabel(session)}
          canActOnProject={isAdmin}
          onDeleted={(res) => {
            // Close first. The server has written a tombstone and nothing more, so
            // there is nothing left to wait on here -- holding the modal open would be
            // waiting on a refetch, not on the delete.
            setShowDelete(false);
            const gone = new Set<string>([session.session_id]);
            // A purge takes the project's other sessions, and this component is never
            // told their ids -- but it is told the project, and every one of them
            // carries it. Dropping by project_hash removes them here rather than
            // leaving them to a refetch: the list is an infinite query holding several
            // pages, and a page that comes back while the frozen order is being reset
            // can carry a row that is already gone. One survivor made it through that
            // way, measured against an API response that no longer had the project.
            const purgedProject = res.purged_sessions > 0 ? res.project_hash : '';
            qc.setQueriesData<{ pages: SessionOverview[][]; pageParams: unknown[] }>(
              { queryKey: ['session-overview'] },
              (old) =>
                old && Array.isArray(old.pages)
                  ? {
                      ...old,
                      pages: old.pages.map((page) =>
                        page.filter(
                          (s) =>
                            !gone.has(s.session_id) &&
                            !(purgedProject && s.project_hash === purgedProject),
                        ),
                      ),
                    }
                  : old,
            );
            qc.invalidateQueries({ queryKey: ['session-overview'], refetchType: 'all' });
            qc.invalidateQueries({ queryKey: ['session-overview-count'], refetchType: 'all' });
            qc.invalidateQueries({ queryKey: ['projects'], refetchType: 'all' });
            if (res.project_blocked) qc.invalidateQueries({ queryKey: ['blocked-projects'] });
            onDeleted?.(session.session_id);
          }}
          onCancel={() => setShowDelete(false)}
        />
      )}
      {isAdmin && showSettings && <BlockedProjectsDialog onClose={() => setShowSettings(false)} />}
      </div>
    </div>
  );
};

export { SessionViewer };
