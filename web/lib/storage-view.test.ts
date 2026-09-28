import { describe, expect, it } from 'vitest';
import { storageViewState } from './storage-view';

describe('storageViewState', () => {
  it('shows error on fetch failure, even with no tables', () => {
    // The key distinction: a 500 must render "error", NOT "empty".
    expect(storageViewState({ isLoading: false, isError: true, tableCount: 0 })).toBe('error');
    expect(storageViewState({ isLoading: false, isError: true, tableCount: 3 })).toBe('error');
  });

  it('shows empty only on a true empty success', () => {
    expect(storageViewState({ isLoading: false, isError: false, tableCount: 0 })).toBe('empty');
  });

  it('shows loading while fetching', () => {
    expect(storageViewState({ isLoading: true, isError: false, tableCount: 0 })).toBe('loading');
  });

  it('shows data when tables are present', () => {
    expect(storageViewState({ isLoading: false, isError: false, tableCount: 3 })).toBe('data');
  });

  it('prioritizes error over loading', () => {
    expect(storageViewState({ isLoading: true, isError: true, tableCount: 0 })).toBe('error');
  });
});
