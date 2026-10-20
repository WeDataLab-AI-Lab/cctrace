import type { SessionOverview } from './types';

const isOverview = (value: unknown, sessionId: string): value is SessionOverview => {
  if (!value || typeof value !== 'object') return false;
  const row = value as Record<string, unknown>;
  return row.session_id === sessionId &&
    typeof row.profile_email === 'string' &&
    typeof row.model === 'string' &&
    typeof row.agent === 'string' &&
    typeof row.start_time === 'string' && Number.isFinite(Date.parse(row.start_time)) &&
    typeof row.end_time === 'string' && Number.isFinite(Date.parse(row.end_time)) &&
    typeof row.has_sync === 'boolean' &&
    ['input_tokens', 'output_tokens', 'cost_usd', 'event_count'].every((key) =>
      typeof row[key] === 'number' && Number.isFinite(row[key]));
};

export const createPersonalNoteNavigator = (
  fetchOverview: (sessionId: string) => Promise<unknown>,
  onFound: (overview: SessionOverview) => void,
  onUnavailable: (sessionId: string) => void,
) => {
  let generation = 0;
  return {
    cancel: () => { generation += 1; },
    open: async (sessionId: string) => {
      const current = ++generation;
      try {
        const overview = await fetchOverview(sessionId);
        if (current !== generation) return;
        if (!isOverview(overview, sessionId)) {
          onUnavailable(sessionId);
          return;
        }
        onFound(overview);
      } catch {
        if (current === generation) onUnavailable(sessionId);
      }
    },
  };
};
