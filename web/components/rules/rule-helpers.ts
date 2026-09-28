import type { ProjectRuleListItem, ProjectRuleStatus } from '@/lib/types';

interface RuleRow {
  key: string;
  rule: ProjectRuleListItem;
}

type StatusFilter = ProjectRuleStatus | 'all';

interface StatusOption {
  value: StatusFilter;
  label: string;
}

const STATUS_OPTIONS: StatusOption[] = [
  { value: 'all', label: 'All statuses' },
  { value: 'active', label: 'Active' },
  { value: 'missing', label: 'Missing' },
  { value: 'deleted', label: 'Deleted' },
  { value: 'unreadable', label: 'Unreadable' },
  { value: 'archived', label: 'Archived' },
];

const agentLabel = (agent: string) =>
  agent === 'claude' ? 'Claude' : agent === 'codex' ? 'Codex' : agent;

const buildRuleRows = (items: ProjectRuleListItem[]): RuleRow[] =>
  items.map(rule => ({ key: String(rule.id), rule }));

const formatDate = (ts: string) => {
  const date = new Date(ts);
  if (Number.isNaN(date.getTime())) return ts;
  return date.toLocaleDateString(undefined, {
    month: '2-digit',
    day: '2-digit',
    year: 'numeric',
  });
};

const formatDateTime = (ts?: string) => {
  if (!ts) return 'Unknown';
  const date = new Date(ts);
  if (Number.isNaN(date.getTime())) return ts;
  return date.toLocaleString(undefined, {
    month: '2-digit',
    day: '2-digit',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
};

const formatBytes = (bytes?: number) => {
  if (!bytes || bytes < 0) return '0 B';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
};

const shortValue = (value?: string) => {
  if (!value) return 'none';
  return value.length > 12 ? `${value.slice(0, 8)}...${value.slice(-4)}` : value;
};

// safeUrl whitelists link/image protocols so user-authored rule markdown cannot
// smuggle `javascript:`/`data:`/`vbscript:` URLs into rendered anchors (stored XSS).
// Relative paths and `#` anchors are allowed; anything else is dropped.
const safeUrl = (url: string): string => {
  const value = (url || '').trim();
  if (value === '') return '';
  if (value.startsWith('#') || value.startsWith('/')) return value;
  try {
    const protocol = new URL(value, 'https://base.invalid').protocol;
    return protocol === 'http:' || protocol === 'https:' || protocol === 'mailto:' ? value : '';
  } catch {
    return '';
  }
};

export {
  STATUS_OPTIONS,
  agentLabel,
  buildRuleRows,
  formatDate,
  formatDateTime,
  formatBytes,
  shortValue,
  safeUrl,
};

export type { RuleRow, StatusFilter, StatusOption };
