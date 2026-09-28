import { FileText, History, MessageSquare } from 'lucide-react';
import { StatusBadge } from './status-badge';
import { DetailField } from './detail-field';
import { MarkdownViewer } from './markdown-viewer';
import { VersionList } from './version-list';
import { CommentSection } from './comment-section';
import { formatDate, formatDateTime, formatBytes, shortValue } from './rule-helpers';
import type { ProjectRuleDetail, ProjectRuleVersion } from '@/lib/types';

interface RuleDetailProps {
  hasSelection: boolean;
  isLoading: boolean;
  isError: boolean;
  detail?: ProjectRuleDetail;
  version?: ProjectRuleVersion;
  selectedVersionId: number;
  commentBody: string;
  canWrite: boolean;
  isCommentPending: boolean;
  onSelectVersion: (versionId: number) => void;
  onCommentBodyChange: (value: string) => void;
  onSubmitComment: () => void;
}

const RuleDetail = ({
  hasSelection,
  isLoading,
  isError,
  detail,
  version,
  selectedVersionId,
  commentBody,
  canWrite,
  isCommentPending,
  onSelectVersion,
  onCommentBodyChange,
  onSubmitComment,
}: RuleDetailProps) => {
  if (hasSelection && isLoading) {
    return (
      <div className="flex h-full items-center justify-center px-6 text-center text-sm text-ink-3">
        Loading rule detail...
      </div>
    );
  }

  if (hasSelection && isError) {
    return (
      <div className="flex h-full items-center justify-center px-6 text-center text-sm text-ink-3">
        Rule detail API unavailable
      </div>
    );
  }

  if (!detail || !version) {
    return (
      <div className="flex h-full items-center justify-center px-6 text-center text-sm text-ink-3">
        Select a rule file
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="border-b border-border px-5 py-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <FileText size={16} className="shrink-0 text-brand" />
              <h3 className="truncate font-mono text-[15px] font-semibold text-ink">
                {detail.rule.rule_path}
              </h3>
            </div>
            <div className="mt-1 text-[12px] text-ink-2">
              {detail.rule.repository_name || detail.rule.project_name || detail.rule.repository_key}
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-3 text-[11px] text-ink-3">
            <StatusBadge status={detail.rule.current_status} />
            <span className="flex items-center gap-1">
              <History size={13} />
              v{version.version_number}
            </span>
            <span className="flex items-center gap-1">
              <MessageSquare size={13} />
              {detail.comments.length}
            </span>
            <span>{formatDate(detail.rule.last_seen_at)}</span>
          </div>
        </div>
      </div>

      <div className="grid min-h-0 flex-1 grid-cols-1 xl:grid-cols-[minmax(0,1fr)_320px]">
        <div className="min-h-0 overflow-y-auto px-6 py-5">
          <div className="mb-5 grid grid-cols-2 gap-3 lg:grid-cols-4">
            <DetailField label="Branch" value={version.branch || 'none'} />
            <DetailField label="Commit" value={shortValue(version.commit_sha)} />
            <DetailField label="Size" value={formatBytes(version.size_bytes)} />
            <DetailField label="Discovered" value={formatDateTime(version.discovered_at)} />
          </div>
          <div className="mb-5 rounded-md border border-border bg-surface px-3 py-2">
            <div className="text-[10px] font-medium uppercase text-ink-3">Change reason</div>
            <div className="mt-1 text-[12px] leading-5 text-ink-2">
              {version.change_reason || 'No change reason'}
            </div>
          </div>
          <MarkdownViewer content={version.content || ''} />
        </div>

        <div className="min-h-0 overflow-y-auto border-t border-border bg-canvas xl:border-l xl:border-t-0">
          <VersionList
            versions={detail.versions}
            selectedVersionId={selectedVersionId}
            currentVersionId={detail.rule.current_version_id}
            onSelectVersion={onSelectVersion}
          />
          <CommentSection
            comments={detail.comments}
            commentBody={commentBody}
            canWrite={canWrite}
            isPending={isCommentPending}
            onCommentBodyChange={onCommentBodyChange}
            onSubmitComment={onSubmitComment}
          />
        </div>
      </div>
    </div>
  );
};

export { RuleDetail };
