import { describe, expect, it } from 'vitest';
import type { AuthUser } from '@/lib/types';
import { canViewUserSessions } from './user-utils';

const me: AuthUser = { id: 1, email: 'b@example.com', role: 'user', name: 'B', cctrace_user_id: 'user-b' };

describe('canViewUserSessions', () => {
  it.each([
    ['admin 은 모든 카드', { ...me, role: 'admin' as const }, { profile_email: 'a@example.com', user_id: 'user-a' }, true],
    ['본인 user_id 일치', me, { profile_email: 'x@example.com', user_id: 'user-b' }, true],
    ['타인 user_id', me, { profile_email: 'a@example.com', user_id: 'user-a' }, false],
    ['cctrace_user_id 있는데 카드 user_id 없음', me, { profile_email: 'b@example.com', user_id: '' }, false],
    ['legacy: cctrace_user_id 없고 이메일 일치', { ...me, cctrace_user_id: undefined }, { profile_email: 'b@example.com', user_id: '' }, true],
    ['legacy: cctrace_user_id 없고 이메일 불일치', { ...me, cctrace_user_id: '' }, { profile_email: 'a@example.com', user_id: '' }, false],
    ['cctrace_user_id 있는데 카드 user_id undefined', me, { profile_email: 'b@example.com' }, false],
    ['이메일 대소문자만 다름: legacy 는 불일치(서버 SQL 이 profile_email = $n 정확 일치, internal/api/access.go)', { ...me, cctrace_user_id: undefined }, { profile_email: 'B@Example.com', user_id: '' }, false],
    ['admin 은 cctrace_user_id 없어도 모든 카드', { ...me, role: 'admin' as const, cctrace_user_id: undefined }, { profile_email: 'a@example.com', user_id: 'user-a' }, true],
    ['로그인 정보 없음', null, { profile_email: 'b@example.com', user_id: 'user-b' }, false],
  ])('%s', (_name, user, card, expected) => {
    expect(canViewUserSessions(user, card)).toBe(expected);
  });
});
