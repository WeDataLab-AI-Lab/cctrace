import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { APITokenManagement, tokenPurposeLabel } from './api-token-management';

const authMock = vi.hoisted(() => ({ mustChangePassword: false }));

vi.mock('@tanstack/react-query', () => ({
  useQuery: () => ({ data: [], isLoading: false }),
  useMutation: () => ({ mutate: () => undefined, isPending: false }),
  useQueryClient: () => ({ invalidateQueries: () => undefined }),
}));

vi.mock('@/components/common/auth-context', () => ({
  useAuth: () => ({ user: { must_change_password: authMock.mustChangePassword } }),
}));

const renderTokens = (mustChangePassword: boolean): string => {
  authMock.mustChangePassword = mustChangePassword;
  return renderToStaticMarkup(createElement(APITokenManagement));
};

// Every token route is refused server-side until the temporary password is
// changed (#559 R6). The form used to accept a name and an expiry first and
// surface the refusal only after submit.
describe('APITokenManagement while the password is temporary', () => {
  it('refuses the create affordance and says why', () => {
    const markup = renderTokens(true);

    expect(markup).toContain('disabled');
    expect(markup).toContain('Change your temporary password below before creating or rotating a token.');
  });

  it('offers creation once the password is settled', () => {
    const markup = renderTokens(false);

    expect(markup).not.toContain('Change your temporary password below before creating or rotating a token.');
  });
});

// cli_read tokens (cctrace auth read) read the API like Settings tokens; they
// were labelled "CLI ingestion" because the label only knew web and api.
describe('tokenPurposeLabel', () => {
  it('labels each token kind by what it can do', () => {
    expect(tokenPurposeLabel('web')).toBe('Read API');
    expect(tokenPurposeLabel('cli_read')).toBe('Read API (CLI)');
    expect(tokenPurposeLabel('api')).toBe('CLI ingestion');
  });
});
