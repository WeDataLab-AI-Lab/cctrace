import type { AdminAIAccount, AdminAILoginStatus } from './types';

/** Why the account cannot be changed from the screen, or null when it can. */
const accountLockReason = (account: AdminAIAccount): string | null => {
  if (account.env_managed) return '환경변수 CODEX_API_KEY 로 관리 중입니다. 계정은 환경변수에서만 바꿀 수 있습니다.';
  if (account.auth_file_is_symlink) {
    return '런타임의 auth.json 이나 홈 디렉터리가 링크라 조회만 가능합니다. 링크된 로그인을 지우지 않도록 변경을 막았습니다.';
  }
  return null;
};

/** One message per refused account change. */
const accountErrorMessage = (code: string): string => {
  if (code === 'runtime_unconfigured') return 'AI 런타임이 설정되지 않아 계정을 관리할 수 없습니다.';
  if (code === 'env_managed') return '환경변수 CODEX_API_KEY 로 관리 중이라 화면에서 바꿀 수 없습니다.';
  if (code === 'auth_file_is_symlink') return 'auth.json 이 링크라 화면에서 바꿀 수 없습니다.';
  if (code === 'login_in_progress') return '진행 중인 로그인이 있습니다. 끝나거나 취소한 뒤 다시 시도해 주세요.';
  if (code === 'run_in_progress') return '리포트 생성 중에는 계정을 바꿀 수 없습니다.';
  if (code === 'session_required') return '계정 관리는 브라우저 로그인 세션에서만 할 수 있습니다.';
  if (code === 'invalid_request') return 'API 키를 입력해 주세요.';
  return '계정을 변경하지 못했습니다. 다시 시도해 주세요.';
};

/** What a failed login's error code adds to the result line. The server sends
 *  codes only; anything else, and a plain failure, adds nothing. */
const loginErrorMessage = (code: string | null): string | null => {
  if (code === 'auth_file_is_symlink') return 'auth.json 이 로그인 도중 링크로 바뀌어 적용하지 않았습니다.';
  if (code === 'runtime_exited') return 'AI 런타임이 로그인 도중 종료됐습니다.';
  return null;
};

/** The verification address as a link target, or null unless it is https. It
 *  comes from the runtime, so a javascript: or data: address never becomes an href. */
const verificationHref = (url: string): string | null => {
  try {
    return new URL(url).protocol === 'https:' ? url : null;
  } catch {
    return null;
  }
};

/** Minutes left until the code expires, measured against the last poll. */
const remainingLabel = (expiresAt: string, now: number): string => {
  const ms = Date.parse(expiresAt) - now;
  if (!(ms > 0)) return '곧 만료';
  return `${Math.ceil(ms / 60_000)}분 남음`;
};

/** What a finished login says; pending has no message. */
const LOGIN_RESULT: Record<Exclude<AdminAILoginStatus, 'pending'>, { text: string; ok: boolean }> = {
  succeeded: { text: 'ChatGPT 로그인을 마쳤습니다.', ok: true },
  failed: { text: 'ChatGPT 로그인에 실패했습니다.', ok: false },
  canceled: { text: '로그인을 취소했습니다.', ok: false },
  expired: { text: '코드 입력 시간이 지났습니다. 다시 시도해 주세요.', ok: false },
};

export { LOGIN_RESULT, accountErrorMessage, accountLockReason, loginErrorMessage, remainingLabel, verificationHref };
