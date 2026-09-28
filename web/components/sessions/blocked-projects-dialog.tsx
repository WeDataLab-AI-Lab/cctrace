'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useAuth } from '@/components/common/auth-context';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import {
  fetchBlockedProjects,
  fetchDeletionPolicy,
  setDeletionPolicy,
  unblockProject,
} from '@/lib/api';

interface BlockedProjectsDialogProps {
  onClose: () => void;
}

/**
 * The standing rules behind the delete button, in one place: which projects are
 * refused, and whether non-admins may delete their own sessions at all.
 *
 * Open to whoever may delete, because blocking is reachable from the delete dialog
 * and a user who can block a project has to be able to unblock it -- otherwise the
 * third step of that dialog is a one-way door. The policy switch stays admin-only:
 * it decides what everyone else may do, so it is not one of them.
 */
const BlockedProjectsDialog = ({ onClose }: BlockedProjectsDialogProps) => {
  const qc = useQueryClient();
  const { isAdmin } = useAuth();

  const { data: blocked, isLoading } = useQuery({
    queryKey: ['blocked-projects'],
    queryFn: fetchBlockedProjects,
  });
  const { data: policy } = useQuery({
    queryKey: ['deletion-policy'],
    queryFn: fetchDeletionPolicy,
  });

  const unblock = useMutation({
    mutationFn: unblockProject,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['blocked-projects'] }),
  });
  const togglePolicy = useMutation({
    mutationFn: setDeletionPolicy,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['deletion-policy'] }),
  });

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[460px] max-w-[460px] gap-0 space-y-3.5 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <header>
          <DialogTitle className="text-[14px] font-semibold text-ink">세션 수집 설정</DialogTitle>
        </header>

        <section className="space-y-2">
          <div className="text-[12px] font-medium text-ink">제외된 프로젝트</div>
          <p className="text-[11px] leading-relaxed text-ink-3 [overflow-wrap:anywhere] [text-wrap:pretty] break-keep">
            여기 있는 프로젝트는 새로 수집되지 않습니다. 이미 저장된 세션은 그대로 있습니다 —
            지우려면 세션별로 삭제하세요.
          </p>
          {isLoading && <p className="text-[11px] leading-relaxed text-ink-3 [overflow-wrap:anywhere] [text-wrap:pretty] break-keep">불러오는 중…</p>}
          {!isLoading && (blocked?.length ?? 0) === 0 && (
            <p className="text-[11px] leading-relaxed text-ink-3 [overflow-wrap:anywhere] [text-wrap:pretty] break-keep">제외된 프로젝트가 없습니다.</p>
          )}
          {(blocked?.length ?? 0) > 0 && (
            <ul className="max-h-[240px] space-y-1 overflow-y-auto">
              {blocked!.map((b) => (
                <li
                  key={b.project_hash}
                  className="flex items-center justify-between gap-3 rounded border border-border px-3 py-2"
                >
                  <div className="min-w-0">
                    <div className="truncate text-[12px] text-ink">
                      {b.project_name || b.project_hash}
                    </div>
                    <div className="truncate text-[10px] text-ink-3">
                      {/* The subpath is the one thing that explains why a repository
                          someone knows by another name is listed here. */}
                      {b.subpaths ? `${b.subpaths} · ` : ''}
                      {b.created_by || '—'} · {b.created_at?.slice(0, 10) || '—'}
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    className="h-7 shrink-0 px-2 text-[12px]"
                    disabled={unblock.isPending}
                    onClick={() => unblock.mutate(b.project_hash)}
                  >
                    해제
                  </Button>
                </li>
              ))}
            </ul>
          )}
          {unblock.isError && (
            <p className="text-[11px] text-danger">{unblock.error.message}</p>
          )}
        </section>

        {isAdmin && (
        <section className="space-y-2 border-t border-border pt-4">
          <div className="text-[12px] font-medium text-ink">일반 사용자의 삭제 권한</div>
          <p className="text-[11px] leading-relaxed text-ink-3 [overflow-wrap:anywhere] [text-wrap:pretty] break-keep">
            켜면 일반 사용자도 본인의 단일 세션을 삭제할 수 있습니다. 끄면 관리자만 세션을 삭제할 수 있습니다.
            프로젝트 전체 삭제·재수집 차단·차단 목록 조회와 해제는 이 설정과 관계없이 관리자만 할 수 있습니다.
          </p>
          <label className="flex items-center gap-2 text-[12px] text-ink">
            <input
              type="checkbox"
              checked={!!policy?.allow_owner_delete}
              disabled={!policy || togglePolicy.isPending}
              onChange={(e) => togglePolicy.mutate(e.target.checked)}
            />
            <span>본인 세션 삭제 허용</span>
          </label>
          {togglePolicy.isError && (
            <p className="text-[11px] text-danger">{togglePolicy.error.message}</p>
          )}
        </section>
        )}

        <div className="flex justify-end pt-0.5">
          <Button className="h-[30px] px-3 text-[12px]" onClick={onClose}>닫기</Button>
        </div>
      </DialogContent>
    </Dialog>
  );
};

export { BlockedProjectsDialog };
