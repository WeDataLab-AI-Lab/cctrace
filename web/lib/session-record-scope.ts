interface SessionRecordScope { profileEmail?: string; loginEmail?: string; userID?: string }
interface SessionRecordQueryIdentity { userId: number; access: 'owner' | 'admin' }
const sessionRecordsQueryKey = (sessionId: string, assembled: boolean, scope?: SessionRecordScope, identity?: SessionRecordQueryIdentity) => {
  const base = ['session-records', sessionId, assembled] as const;
  if (!scope && !identity) return base;
  return [...base, 'scoped', identity?.userId ?? null, identity?.access ?? '', scope?.profileEmail ?? '', scope?.loginEmail ?? '', scope?.userID ?? ''] as const;
};
export { sessionRecordsQueryKey };
export type { SessionRecordScope, SessionRecordQueryIdentity };
