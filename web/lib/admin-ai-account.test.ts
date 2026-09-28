import { describe, expect, it } from 'vitest';

import { accountErrorMessage, accountLockReason, loginErrorMessage, remainingLabel, verificationHref } from './admin-ai-account';
import type { AdminAIAccount } from './types';

const account = (over: Partial<AdminAIAccount> = {}): AdminAIAccount => ({
  auth_mode: 'chatgpt',
  email: 'ops@example.com',
  plan_type: 'pro',
  env_managed: false,
  auth_file_is_symlink: false,
  login: null,
  ...over,
});

describe('accountLockReason', () => {
  it('allows changes when neither the environment nor a link owns the login', () => {
    expect(accountLockReason(account())).toBeNull();
  });

  it('names the environment variable when it manages the key', () => {
    expect(accountLockReason(account({ env_managed: true, auth_file_is_symlink: true }))).toContain('CODEX_API_KEY');
  });

  it('explains a linked auth file', () => {
    expect(accountLockReason(account({ auth_file_is_symlink: true }))).toContain('링크');
  });
});

describe('accountErrorMessage', () => {
  it('gives each refusal its own message', () => {
    const codes = [
      'runtime_unconfigured',
      'env_managed',
      'auth_file_is_symlink',
      'login_in_progress',
      'run_in_progress',
      'session_required',
      'invalid_request',
      'login_failed',
    ];
    expect(new Set(codes.map(accountErrorMessage)).size).toBe(codes.length);
  });
});

describe('remainingLabel', () => {
  const now = Date.parse('2026-09-15T03:00:00Z');

  it('rounds the minutes left up', () => {
    expect(remainingLabel('2026-09-15T03:14:01Z', now)).toBe('15분 남음');
    expect(remainingLabel('2026-09-15T03:00:30Z', now)).toBe('1분 남음');
  });

  it('does not count below zero', () => {
    expect(remainingLabel('2026-09-15T02:59:00Z', now)).toBe('곧 만료');
    expect(remainingLabel('not a date', now)).toBe('곧 만료');
  });
});

describe('run_in_progress', () => {
  it('says a running report blocks the change', () => {
    expect(accountErrorMessage('run_in_progress')).toBe('리포트 생성 중에는 계정을 바꿀 수 없습니다.');
  });
});

describe('loginErrorMessage', () => {
  it('explains the codes a failed login carries', () => {
    expect(loginErrorMessage('auth_file_is_symlink')).toContain('링크');
    expect(loginErrorMessage('runtime_exited')).toContain('종료');
  });

  it('adds nothing for a plain failure, no error, or unknown text', () => {
    expect(loginErrorMessage('login_failed')).toBeNull();
    expect(loginErrorMessage(null)).toBeNull();
    expect(loginErrorMessage('authorization denied for ops@example.com')).toBeNull();
  });
});

describe('verificationHref', () => {
  it('links an https address', () => {
    expect(verificationHref('https://auth.openai.com/codex/device')).toBe('https://auth.openai.com/codex/device');
  });

  it('refuses any other scheme or an unparsable address', () => {
    for (const url of ['javascript:alert(1)', 'http://auth.openai.com/codex/device', 'data:text/html,x', '//evil.test', 'not a url', '']) {
      expect(verificationHref(url)).toBeNull();
    }
  });
});
