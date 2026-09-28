import { describe, expect, it } from 'vitest';
import { deepLinkSelection, selectionProjectHashes, type ProjectSelection } from './project-selection';

const selection: ProjectSelection = { key: 'repo-1', name: 'repo', hashes: ['h-main', 'h-worktree'] };

describe('selectionProjectHashes', () => {
  it('resolves against the live registry while it still holds the project', () => {
    const hashes = selectionProjectHashes(selection, [
      { project_hash: 'h-main', repository_id: 'repo-1' },
      { project_hash: 'h-worktree', repository_id: 'repo-1' },
      { project_hash: 'h-new', repository_id: 'repo-1' },
      { project_hash: 'h-other', repository_id: 'repo-2' },
    ]);
    // A worktree registered after the selection was made is picked up.
    expect(hashes).toEqual(['h-main', 'h-worktree', 'h-new']);
  });

  // The fixed defect. Drop the fallback (`return live`) and this returns [], which the
  // request builder omits from the URL and the server reads as "no project filter": the
  // list widens to every project under a picker still naming one.
  it('never widens to every project when the scope no longer holds the selection', () => {
    expect(selectionProjectHashes(selection, [{ project_hash: 'h-other', repository_id: 'repo-2' }]))
      .toEqual(['h-main', 'h-worktree']);
  });

  // Same defect, seen while the rescoped registry query is still in flight.
  it('holds the selection while the scoped registry is undefined', () => {
    expect(selectionProjectHashes(selection, undefined)).toEqual(['h-main', 'h-worktree']);
  });

  it('filters by nothing when nothing is selected', () => {
    expect(selectionProjectHashes(null, [{ project_hash: 'h-main', repository_id: 'repo-1' }])).toEqual([]);
  });
});

describe('deepLinkSelection', () => {
  const registry = [
    { project_hash: 'h-main', repository_id: 'repo-1', repository_name: 'repo' },
    { project_hash: 'h-worktree', repository_id: 'repo-1', repository_name: 'repo' },
    { project_hash: 'h-other', repository_id: 'repo-2', repository_name: 'other' },
  ];

  // The caller applies the link once, so the selection has to carry every sibling hash
  // itself: drop the sibling collection (`hashes: [match.project_hash]`) and a later
  // scope change strands the worktree's sessions outside the filter.
  it('carries every hash sharing the linked project identity', () => {
    expect(deepLinkSelection('h-worktree', registry)).toEqual({
      key: 'repo-1',
      name: 'repo',
      hashes: ['h-main', 'h-worktree'],
    });
  });

  // A miss is permanent, so it must leave the selection alone rather than invent one.
  it('resolves nothing for a hash the registry does not know', () => {
    expect(deepLinkSelection('h-missing', registry)).toBeNull();
    expect(deepLinkSelection('h-main', undefined)).toBeNull();
  });
});
