import { Button } from '@/components/ui/button';
import { formatDate } from './rule-helpers';
import type { ProjectRuleComment } from '@/lib/types';

interface CommentSectionProps {
  comments: ProjectRuleComment[];
  commentBody: string;
  canWrite: boolean;
  isPending: boolean;
  onCommentBodyChange: (value: string) => void;
  onSubmitComment: () => void;
}

const CommentSection = ({
  comments,
  commentBody,
  canWrite,
  isPending,
  onCommentBodyChange,
  onSubmitComment,
}: CommentSectionProps) => {
  const handleTextareaChange = (event: React.ChangeEvent<HTMLTextAreaElement>) => {
    onCommentBodyChange(event.target.value);
  };

  const handleSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    onSubmitComment();
  };

  return (
    <>
      <div className="border-b border-border px-4 py-3">
        <div className="text-[12px] font-medium uppercase text-ink-3">Comments</div>
        <div className="mt-2 space-y-2">
          {comments.length === 0 ? (
            <div className="rounded-md border border-border bg-surface px-3 py-3 text-[12px] text-ink-3">
              No comments
            </div>
          ) : comments.map(comment => (
            <div key={comment.id} className="rounded-md border border-border bg-surface px-3 py-2">
              <div className="flex items-center justify-between gap-2 text-[10px] uppercase text-ink-3">
                <span className="truncate">{comment.author_profile_email || comment.author_user_id || 'Unknown'}</span>
                <span className="shrink-0">{formatDate(comment.created_at)}</span>
              </div>
              <div className="mt-1 text-[12px] leading-5 text-ink-2">{comment.body}</div>
            </div>
          ))}
        </div>
      </div>

      <div className="space-y-3 px-4 py-3">
        <form className="space-y-2" onSubmit={handleSubmit}>
          <textarea
            value={commentBody}
            onChange={handleTextareaChange}
            disabled={!canWrite || isPending}
            placeholder="Add comment"
            className="min-h-[76px] w-full resize-none rounded-md border border-border bg-surface px-3 py-2 text-[12px] text-ink outline-none placeholder:text-ink-4 focus:border-brand disabled:bg-surface-sunk"
          />
          <Button
            type="submit"
            disabled={!canWrite || !commentBody.trim() || isPending}
            className="h-[30px] w-full rounded-md bg-brand px-3 text-[12px] font-medium text-brand-ink hover:bg-brand disabled:cursor-not-allowed disabled:bg-border disabled:opacity-100"
          >
            {isPending ? 'Saving...' : 'Add comment'}
          </Button>
        </form>
      </div>
    </>
  );
};

export { CommentSection };
