import { cn } from '@/lib/utils';
import { repoStatusDescription, repoStatusText, type FrameRepoStatus, type RepoTone } from './model';

/**
 * 하단 상태 표시줄(A6): 선택한 세션 저장소의 "브랜치 ● 미커밋 N건". 오른쪽에 붙인다.
 * 점 색은 미커밋이 있으면 앰버, 없으면 초록, 모르면 점선 고리다. 모르는 것을 "없음"(초록)으로 그리지 않는다.
 */

const DOT_CLASS: Record<RepoTone, string> = {
  clean: 'bg-success',
  dirty: 'bg-warning',
  unknown: 'border border-dashed border-ink-3',
};

interface FrameStatusBarProps {
  repo: FrameRepoStatus;
  className?: string;
}

const FrameStatusBar = ({ repo, className }: FrameStatusBarProps) => {
  const text = repoStatusText(repo);
  const description = repoStatusDescription(repo);
  return (
    <div
      role="status"
      aria-label="저장소 상태"
      aria-description={description}
      title={description}
      className={cn('flex h-[22px] shrink-0 items-center border-t border-border bg-surface-2 px-3 text-[11px] text-ink-2', className)}
    >
      <span className="ml-auto inline-flex items-center gap-1.5">
        <span className="font-mono" title="브랜치">
          {text.branch}
        </span>
        <span aria-hidden="true" className={cn('inline-block size-2 shrink-0 rounded-full', DOT_CLASS[text.tone])} />
        <span className={cn(text.tone === 'dirty' && 'font-medium text-ink', text.tone === 'unknown' && 'text-ink-3')}>{text.uncommitted}</span>
      </span>
    </div>
  );
};

export { FrameStatusBar };
