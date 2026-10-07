import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { describe, expect, it } from 'vitest';
import { UserDetail } from './user-detail';

const render = (canViewSessions: boolean) => {
  const client = new QueryClient();
  return renderToStaticMarkup(
    createElement(
      QueryClientProvider,
      { client },
      createElement(UserDetail, {
        email: 'a@example.com',
        avatarColor: 'red',
        displayName: 'a',
        viewMode: 'cost',
        canViewSessions,
      }),
    ),
  );
};

describe('UserDetail Recent Sessions', () => {
  it('canViewSessions=false 면 안내 문구만 보이고 세션 표는 없다 (#799)', () => {
    const html = render(false);
    expect(html).toContain('Recent Sessions');
    expect(html).toContain('다른 사용자의 세션은 볼 수 없습니다');
    expect(html).not.toContain('No sessions');
    expect(html).not.toContain('Session ID');
  });

  it('canViewSessions=true 면 기존 렌더를 유지한다', () => {
    const html = render(true);
    expect(html).toContain('Recent Sessions');
    expect(html).toContain('No sessions');
    expect(html).not.toContain('다른 사용자의 세션은 볼 수 없습니다');
  });
});
