export type StorageView = 'loading' | 'error' | 'empty' | 'data';

/**
 * Decides what the /storage page should render, keeping a fetch FAILURE
 * ('error') distinct from a genuine empty success ('empty') — react-query
 * throws on non-2xx, so a backend outage must not be mistaken for "no data".
 * Precedence: error > loading > empty > data.
 */
export function storageViewState(opts: {
  isLoading: boolean;
  isError: boolean;
  tableCount: number;
}): StorageView {
  if (opts.isError) return 'error';
  if (opts.isLoading) return 'loading';
  if (opts.tableCount === 0) return 'empty';
  return 'data';
}
