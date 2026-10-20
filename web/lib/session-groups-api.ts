import { apiFetch, buildUrl } from './api';
import type { SessionClassification } from './session-analysis-types';

export interface SessionGroupScope {
  source?: string;
  projectHashes?: readonly string[];
  agent?: string;
  assembled?: boolean;
  classification?: SessionClassification;
  loginEmail?: string;
  since?: string;
  until?: string;
  includeAnalysis?: boolean;
  query?: string;
  plan?: 'file' | 'mode';
  category?: string;
  sort?: 'start' | 'activity' | 'cost' | 'requests';
  bucket?: 'done' | 'leftover' | 'ended_midway' | 'unknown';
}

export interface SessionGroupMember {
  session_id: string;
  ended_at: string;
  dismissed: boolean;
}

export interface SessionGroups {
  scope: 'filtered_total';
  grouping_version: number;
  truncated: boolean;
  overview_total: number;
  running: Array<{ session_id: string; category: string; bucket: string }>;
  ended: Array<{ key: string; ended_at: string; total: number; items: SessionGroupMember[] }>;
  counts: {
    running: number;
    ended: number;
    ended_groups: number;
    running_by: Record<string, number>;
  };
}

export interface SessionGroupMemberPage {
  group_key: string;
  grouping_version: number;
  total: number;
  items: SessionGroupMember[];
  next_cursor: string;
}

export class SessionGroupError extends Error {
  constructor(public readonly status: number) {
    super(status === 409 ? '세션 상태가 바뀌었습니다. 목록을 다시 확인해 주세요.' : '세션 묶음을 읽거나 변경하지 못했습니다.');
  }
}

const groupUrl = (scope: SessionGroupScope, path = '/api/me/session-groups', params: Record<string, string> = {}) => {
  if (scope.projectHashes !== undefined && scope.projectHashes.length === 0) throw new Error('project identity member set is empty');
  return buildUrl(path, {
    source: scope.source ?? '',
    project_hashes: scope.projectHashes?.join(',') ?? '',
    agent: scope.agent ?? '',
    assembled: scope.assembled ? '1' : '0',
    classification: scope.classification ?? '',
    login_email: scope.loginEmail ?? '',
    since: scope.since ?? '',
    until: scope.until ?? '',
    include: scope.includeAnalysis ? 'analysis' : '',
    q: scope.query ?? '',
    plan: scope.plan ?? '',
    category: scope.category ?? '',
    sort: scope.sort ?? '',
    bucket: scope.bucket ?? '',
    ...params,
  });
};

const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
const count = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
const identity = (value: unknown): value is string => typeof value === 'string' && value.length > 0;
const epoch = (value: unknown): value is string => identity(value) && Number.isFinite(Date.parse(value));
const member = (value: unknown): value is SessionGroupMember => record(value) && identity(value.session_id) && epoch(value.ended_at) && typeof value.dismissed === 'boolean';
const members = (value: unknown): value is SessionGroupMember[] => Array.isArray(value) && value.every(member);
const validOverview = (value: unknown): value is SessionGroups => {
  if (!record(value) || value.scope !== 'filtered_total' || !count(value.grouping_version) ||
      typeof value.truncated !== 'boolean' || !count(value.overview_total) ||
      !Array.isArray(value.running) || !Array.isArray(value.ended) || !record(value.counts)) return false;
  const counts = value.counts;
  return count(counts.running) && count(counts.ended) && count(counts.ended_groups) &&
    record(counts.running_by) && Object.values(counts.running_by).every(count) &&
    value.running.every((item: unknown) => record(item) && identity(item.session_id) && typeof item.category === 'string' && typeof item.bucket === 'string') &&
    value.ended.every((group: unknown) => record(group) && identity(group.key) && epoch(group.ended_at) && count(group.total) && members(group.items));
};
const validMemberPage = (value: unknown): value is SessionGroupMemberPage => record(value) &&
  identity(value.group_key) && count(value.grouping_version) && count(value.total) &&
  members(value.items) && typeof value.next_cursor === 'string';

const response = async (res: Response): Promise<unknown> => {
  if (!res.ok) throw new SessionGroupError(res.status);
  try { return await res.json(); } catch { throw new SessionGroupError(res.status); }
};

export const fetchSessionGroups = async (scope: SessionGroupScope): Promise<SessionGroups> => {
  const data = await response(await apiFetch(groupUrl(scope)));
  if (!validOverview(data)) throw new SessionGroupError(409);
  return data;
};

export const fetchSessionGroupMembers = async (scope: SessionGroupScope, key: string, cursor = ''): Promise<SessionGroupMemberPage> => {
  const data = await response(await apiFetch(groupUrl(scope, '/api/me/session-groups/members', { group_key: key, cursor, limit: '100' })));
  if (!validMemberPage(data)) throw new SessionGroupError(409);
  return data;
};

// Read the full server membership before any write. A bounded overview may omit
// members even when its group header and counts are present.
export const fetchCompleteSessionGroup = async (
  scope: SessionGroupScope,
  key: string,
  version: number,
  expectedTotal: number,
): Promise<SessionGroupMember[]> => {
  const items: SessionGroupMember[] = [];
  const seenItems = new Set<string>();
  const seenCursors = new Set<string>();
  let cursor = '';
  do {
    const page = await fetchSessionGroupMembers(scope, key, cursor);
    if (page.group_key !== key || page.grouping_version !== version || page.total !== expectedTotal || page.items.length === 0 || page.items.length > 100) throw new SessionGroupError(409);
    for (const item of page.items) {
      if (seenItems.has(item.session_id) || !item.ended_at) throw new SessionGroupError(409);
      seenItems.add(item.session_id);
      items.push(item);
    }
    if (items.length > expectedTotal || page.next_cursor !== '' && (page.next_cursor === cursor || seenCursors.has(page.next_cursor))) throw new SessionGroupError(409);
    cursor = page.next_cursor;
    if (cursor) seenCursors.add(cursor);
  } while (cursor);
  if (items.length !== expectedTotal) throw new SessionGroupError(409);
  return items;
};

export const updateSessionGroupDismissals = async (
  scope: SessionGroupScope,
  groupingVersion: number,
  items: readonly Pick<SessionGroupMember, 'session_id' | 'ended_at'>[],
  dismiss: boolean,
  groupKey = '',
): Promise<void> => {
  if (items.length === 0 || items.length > 100) throw new Error('Select between 1 and 100 group members');
  const res = await apiFetch(groupUrl(scope, '/api/me/session-groups/dismiss'), {
    method: dismiss ? 'PUT' : 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ grouping_version: groupingVersion, ...(groupKey ? { group_key: groupKey } : {}), items }),
  });
  const data = await response(res);
  if (!record(data) || data.acknowledged !== true || !count(data.count) || data.count !== items.length ||
      !count(data.grouping_version) || data.grouping_version !== groupingVersion) throw new SessionGroupError(res.status);
};
