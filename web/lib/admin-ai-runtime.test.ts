import { describe, expect, it } from 'vitest';

import {
  RUNTIME_LABEL,
  credentialErrorMessage,
  credentialLockReason,
  credentialSummary,
  enableBlockReason,
  enabledErrorMessage,
  runtimeErrorMessage,
  runtimeState,
  sharedAccountWarning,
} from './admin-ai-runtime';
import type { AdminAICredential, AdminAIRuntime } from './types';

const runtime = (over: Partial<AdminAIRuntime> = {}): AdminAIRuntime => ({
  key: 'openai-api',
  implemented: true,
  configured: true,
  available: true,
  reason: null,
  selected: false,
  provider: 'openai',
  auth_mode: 'api_key',
  account_email: '',
  plan_type: '',
  used_percent: null,
  personal_account_warning: false,
  credential: null,
  ...over,
});

const credential = (over: Partial<AdminAICredential> = {}): AdminAICredential => ({
  provider: 'openai',
  source: 'admin',
  key_hint: 'abcd',
  env_var: 'CCTRACE_AI_OPENAI_API_KEY',
  reason: null,
  updated_at: null,
  ...over,
});

describe('RUNTIME_LABEL', () => {
  it('names the runtimes', () => {
    expect(RUNTIME_LABEL).toEqual({
      'codex-app-server': 'Codex app-server',
      'openai-api': 'OpenAI API',
      'claude-api': 'Claude API',
      'nvidia-api': 'NVIDIA API',
      'litellm-api': 'LiteLLM API',
    });
  });
});

describe('runtimeState', () => {
  it('is connected when configured and available', () => {
    expect(runtimeState(runtime())).toEqual({ text: '연결됨', ok: true });
  });

  it('says unavailable with the reason', () => {
    expect(runtimeState(runtime({ available: false, reason: '로그인 필요' }))).toEqual({
      text: '사용 불가 · 로그인 필요',
      ok: false,
    });
  });

  it('says unconfigured before anything else', () => {
    expect(runtimeState(runtime({ configured: false, available: false, reason: 'API 키 없음' }))).toEqual({
      text: '미설정 · API 키 없음',
      ok: false,
    });
    expect(runtimeState(runtime({ configured: false, available: false })).text).toBe('미설정');
  });
});

describe('sharedAccountWarning', () => {
  it('warns about one person account for codex with ChatGPT login', () => {
    const rt = runtime({ key: 'codex-app-server', provider: 'codex', auth_mode: 'chatgpt', personal_account_warning: true });
    expect(sharedAccountWarning(rt)).toContain('한 사람의 계정');
  });

  it('warns about one API key billed to its organization for any API key runtime', () => {
    for (const rt of [runtime(), runtime({ key: 'claude-api', provider: 'anthropic' }), runtime({ key: 'codex-app-server', provider: 'codex' })]) {
      const text = sharedAccountWarning(rt);
      expect(text).toContain('API 키');
      expect(text).toContain('조직');
    }
  });

  it('says nothing without an authenticated runtime', () => {
    expect(sharedAccountWarning(undefined)).toBeNull();
    expect(sharedAccountWarning(runtime({ auth_mode: 'none' }))).toBeNull();
  });
});

describe('credentialSummary', () => {
  it('names the environment variable, the admin key hint, or nothing', () => {
    expect(credentialSummary(credential({ source: 'env', key_hint: 'wxyz' }))).toBe('환경변수 CCTRACE_AI_OPENAI_API_KEY · …wxyz');
    expect(credentialSummary(credential())).toBe('관리자 등록 · …abcd');
    expect(credentialSummary(credential({ source: 'none', key_hint: null }))).toBe('없음');
  });
});

describe('credentialLockReason', () => {
  it('locks only an environment managed key, naming the variable', () => {
    expect(credentialLockReason(credential({ source: 'env' }))).toBe('환경변수 CCTRACE_AI_OPENAI_API_KEY 로 관리 중');
    expect(credentialLockReason(credential())).toBeNull();
    expect(credentialLockReason(credential({ source: 'none' }))).toBeNull();
  });
});

describe('runtimeErrorMessage', () => {
  it('uses the server reason for an unconfigured runtime', () => {
    expect(runtimeErrorMessage('runtime_unconfigured', 'API 키가 없습니다')).toBe('API 키가 없습니다');
    expect(runtimeErrorMessage('runtime_unconfigured', '')).toContain('설정되지');
  });

  it('gives each refusal its own message', () => {
    const codes = ['run_in_progress', 'unknown_runtime', 'session_required', 'runtime_unconfigured', 'http_500'];
    expect(new Set(codes.map((c) => runtimeErrorMessage(c, ''))).size).toBe(codes.length);
    expect(runtimeErrorMessage('run_in_progress', '')).toBe('리포트 생성 중에는 런타임을 바꿀 수 없습니다.');
  });
});

describe('enableBlockReason', () => {
  it("gives the server's reason: nothing chosen, or the chosen one cannot run", () => {
    expect(enableBlockReason({ selection_reason: '런타임이 선택되지 않았습니다' })).toBe('런타임이 선택되지 않았습니다');
    expect(enableBlockReason({ selection_reason: '선택한 런타임을 사용할 수 없습니다: codex CLI 없음' })).toBe(
      '선택한 런타임을 사용할 수 없습니다: codex CLI 없음',
    );
    expect(enableBlockReason({ selection_reason: null })).toBeNull();
  });
});

describe('enabledErrorMessage', () => {
  it('gives each refusal its own message', () => {
    const codes = ['runtime_not_selected', 'runtime_not_configured', 'session_required', 'invalid_request', 'http_500'];
    expect(new Set(codes.map(enabledErrorMessage)).size).toBe(codes.length);
  });
});

describe('credentialErrorMessage', () => {
  it('gives each refusal its own message', () => {
    const codes = ['invalid_request', 'env_managed', 'secrets_unavailable', 'credential_store_failed', 'session_required', 'http_500'];
    expect(new Set(codes.map(credentialErrorMessage)).size).toBe(codes.length);
  });
});
