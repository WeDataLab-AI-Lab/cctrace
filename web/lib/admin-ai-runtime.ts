import type { AdminAICredential, AdminAIResponse, AdminAIRuntime } from './types';

const RUNTIME_LABEL: Record<string, string> = {
  'codex-app-server': 'Codex app-server',
  'openai-api': 'OpenAI API',
  'claude-api': 'Claude API',
  'nvidia-api': 'NVIDIA API',
  'litellm-api': 'LiteLLM API',
};

/** Unconfigured comes first: an unconfigured runtime is never available either. */
const runtimeState = (rt: AdminAIRuntime): { text: string; ok: boolean } => {
  const withReason = (text: string) => (rt.reason ? `${text} · ${rt.reason}` : text);
  if (!rt.configured) return { text: withReason('미설정'), ok: false };
  if (!rt.available) return { text: withReason('사용 불가'), ok: false };
  return { text: '연결됨', ok: true };
};

/** Who pays for and whose account runs everyone's analysis, for the selected runtime. */
const sharedAccountWarning = (rt: AdminAIRuntime | undefined): string | null => {
  if (rt?.personal_account_warning) return '한 사람의 계정으로 모든 사용자의 분석이 처리됩니다.';
  if (rt?.auth_mode === 'api_key') {
    return '하나의 API 키로 모든 사용자의 분석이 처리되며, 비용은 그 키가 속한 조직에 청구됩니다.';
  }
  return null;
};

/** Where the key comes from and its last 4 characters. */
const credentialSummary = (cred: AdminAICredential): string => {
  const source = cred.source === 'env' ? `환경변수 ${cred.env_var}` : cred.source === 'admin' ? '관리자 등록' : '없음';
  return cred.key_hint ? `${source} · …${cred.key_hint}` : source;
};

/** Why the key cannot be changed from the screen, or null when it can. */
const credentialLockReason = (cred: AdminAICredential): string | null =>
  cred.source === 'env' ? `환경변수 ${cred.env_var} 로 관리 중` : null;

/** One message per refused runtime change; an unconfigured runtime says why in the server's words. */
const runtimeErrorMessage = (code: string, detail: string): string => {
  if (code === 'runtime_unconfigured') return detail || '런타임이 설정되지 않아 선택할 수 없습니다.';
  if (code === 'run_in_progress') return '리포트 생성 중에는 런타임을 바꿀 수 없습니다.';
  if (code === 'unknown_runtime') return '알 수 없는 런타임입니다.';
  if (code === 'session_required') return '런타임 선택은 브라우저 로그인 세션에서만 할 수 있습니다.';
  return '런타임을 바꾸지 못했습니다. 다시 시도해 주세요.';
};

/** Why AI reports cannot be turned on now, in the server's words, or null when they can. */
const enableBlockReason = (data: Pick<AdminAIResponse, 'selection_reason'>): string | null => data.selection_reason;

/** One message per refused switch change. */
const enabledErrorMessage = (code: string): string => {
  if (code === 'runtime_not_selected') return '이 서버에서 쓸 수 있는 런타임을 먼저 선택하세요.';
  if (code === 'runtime_not_configured') return '선택한 런타임이 설정되지 않아 켤 수 없습니다.';
  if (code === 'session_required') return 'AI 리포트 사용 설정은 브라우저 로그인 세션에서만 바꿀 수 있습니다.';
  if (code === 'invalid_request') return '요청 형식이 올바르지 않습니다.';
  return 'AI 리포트 사용 설정을 바꾸지 못했습니다. 다시 시도해 주세요.';
};

/** One message per refused key change. */
const credentialErrorMessage = (code: string): string => {
  if (code === 'invalid_request') return 'API 키를 입력해 주세요.';
  if (code === 'env_managed') return '환경변수로 관리 중이라 화면에서 바꿀 수 없습니다.';
  if (code === 'secrets_unavailable') return '서버에 키 암호화 설정이 없어 키를 저장할 수 없습니다.';
  if (code === 'credential_store_failed') return '키를 저장하지 못했습니다. 다시 시도해 주세요.';
  if (code === 'session_required') return 'API 키 관리는 브라우저 로그인 세션에서만 할 수 있습니다.';
  return 'API 키를 변경하지 못했습니다. 다시 시도해 주세요.';
};

export {
  RUNTIME_LABEL,
  credentialErrorMessage,
  credentialLockReason,
  credentialSummary,
  enableBlockReason,
  enabledErrorMessage,
  runtimeErrorMessage,
  runtimeState,
  sharedAccountWarning,
};
