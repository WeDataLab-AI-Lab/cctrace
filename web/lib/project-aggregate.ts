import { projectIdentityKey, projectIdentityLabel, type ProjectIdentityInput } from './project-identity';

interface ProjectAggregateRow {
  project_hash?: string;
  project_hashes?: readonly string[] | null;
}

const aggregateProjectHashes = (row: ProjectAggregateRow): string[] => {
  const hashes: string[] = [];
  for (const rawHash of row.project_hashes ?? []) {
    const hash = rawHash.trim();
    if (hash !== '' && !hashes.includes(hash)) hashes.push(hash);
  }

  const representative = row.project_hash?.trim() ?? '';
  if (representative !== '' && !hashes.includes(representative)) hashes.unshift(representative);
  return hashes;
};

const matchingRegistryProject = (
  row: ProjectAggregateRow,
  registry: readonly ProjectIdentityInput[],
): ProjectIdentityInput | undefined => {
  const hashes = new Set(aggregateProjectHashes(row));
  if (hashes.size === 0) return undefined;
  return registry.find((project) => hashes.has(project.project_hash?.trim() ?? ''));
};

const projectAggregateKey = (row: ProjectAggregateRow, registry: readonly ProjectIdentityInput[]): string => {
  const match = matchingRegistryProject(row, registry);
  if (match) return projectIdentityKey(match);
  const hashes = aggregateProjectHashes(row);
  return hashes.length > 0 ? `aggregate:${hashes.join(',')}` : '__unknown_project__';
};

const projectAggregateLabel = (row: ProjectAggregateRow, registry: readonly ProjectIdentityInput[]): string => {
  const match = matchingRegistryProject(row, registry);
  if (!match || (!match.repository_name?.trim() && !match.repository_id?.trim())) return 'Unknown Project';
  return projectIdentityLabel(match);
};

const projectAggregateHref = (row: ProjectAggregateRow): string | null => {
  const hashes = aggregateProjectHashes(row);
  const representative = row.project_hash?.trim() || hashes[0] || '';
  return representative === '' ? null : `/sessions?project_hash=${encodeURIComponent(representative)}`;
};

export { aggregateProjectHashes, projectAggregateHref, projectAggregateKey, projectAggregateLabel };
export type { ProjectAggregateRow };
