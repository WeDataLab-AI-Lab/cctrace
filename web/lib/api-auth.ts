type AuthFailureHandler = () => void;
type RetryRequest = () => Promise<Response>;

type RefreshResult =
  | { readonly kind: 'refreshed' }
  | { readonly kind: 'auth_expired' }
  | { readonly kind: 'temporary_failure' };

const REFRESH_AUTH_EXPIRED_STATUSES = [401, 403] as const;

let refreshPromise: Promise<RefreshResult> | null = null;
let onAuthFailure: AuthFailureHandler | null = null;

const setAuthFailureHandler = (handler: AuthFailureHandler | null): void => {
  onAuthFailure = handler;
};

const isAuthExpiredStatus = (status: number): boolean =>
  REFRESH_AUTH_EXPIRED_STATUSES.some((expiredStatus) => expiredStatus === status);

const isFetchFailure = (error: unknown): boolean =>
  error instanceof Error || (typeof DOMException !== 'undefined' && error instanceof DOMException);

const assertNever = (value: never): never => {
  throw new Error(`Unhandled refresh result: ${JSON.stringify(value)}`);
};

const performRefresh = async (refreshUrl: string): Promise<RefreshResult> => {
  try {
    const res = await fetch(refreshUrl, {
      method: 'POST',
      credentials: 'include',
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });

    if (res.ok) {
      return { kind: 'refreshed' };
    }
    if (isAuthExpiredStatus(res.status)) {
      return { kind: 'auth_expired' };
    }
    return { kind: 'temporary_failure' };
  } catch (error) {
    if (isFetchFailure(error)) {
      return { kind: 'temporary_failure' };
    }
    throw error;
  }
};

const clearRefreshPromise = (): void => {
  queueMicrotask(() => {
    refreshPromise = null;
  });
};

const ensureRefreshed = (refreshUrl: string): Promise<RefreshResult> => {
  if (!refreshPromise) {
    refreshPromise = performRefresh(refreshUrl).finally(clearRefreshPromise);
  }
  return refreshPromise;
};

const notifyAuthFailure = (): void => {
  onAuthFailure?.();
};

const refreshAfterUnauthorized = async (
  refreshUrl: string,
  retryRequest: RetryRequest,
): Promise<Response | null> => {
  const result = await ensureRefreshed(refreshUrl);

  switch (result.kind) {
    case 'refreshed':
      return retryRequest();
    case 'auth_expired':
      notifyAuthFailure();
      return null;
    case 'temporary_failure':
      return null;
    default:
      return assertNever(result);
  }
};

export { refreshAfterUnauthorized, setAuthFailureHandler };
