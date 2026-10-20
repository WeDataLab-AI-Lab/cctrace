// The conversation row uses a 24px avatar and a 20px first text line. Both
// speakers share the same transparent 1px border, so this offset centers the
// avatar against the first line for user and agent turns alike.
const CONVERSATION_AVATAR_ALIGNMENT = 'mt-[7px]';

// Browser locale and time zone: the reader's own clock, the same one their
// shell and agent used when the turn was recorded.
const conversationTimeFormat = new Intl.DateTimeFormat(undefined, {
  year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit',
});
const formatConversationTime = (ts?: string): { label: string; iso: string } | undefined => {
  if (!ts) return undefined;
  const at = new Date(ts);
  if (Number.isNaN(at.getTime())) return undefined;
  return { label: conversationTimeFormat.format(at), iso: at.toISOString() };
};

export { CONVERSATION_AVATAR_ALIGNMENT, formatConversationTime };
