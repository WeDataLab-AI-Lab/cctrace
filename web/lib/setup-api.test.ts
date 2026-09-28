import { afterEach, expect, it, vi } from 'vitest';
import { setup } from './api';

afterEach(() => vi.unstubAllGlobals());

it('sends the setup token only in the header and preserves CSRF headers', async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ user: {} }), { status: 201 }));
  vi.stubGlobal('fetch', fetchMock);
  await setup('admin@example.com', 'Admin', 'password123', 'install-secret');
  const [url, options] = fetchMock.mock.calls[0];
  expect(options.headers['X-Setup-Token']).toBe('install-secret');
  expect(options.headers['X-Requested-With']).toBe('XMLHttpRequest');
  expect(options.body).not.toContain('install-secret');
  expect(url).not.toContain('install-secret');
});

it('explains forbidden setup requests', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{}', { status: 403 })));
  await expect(setup('admin@example.com', 'Admin', 'password123', 'wrong')).rejects.toThrow('server logs');
});
