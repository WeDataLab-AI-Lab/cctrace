import type { AdminAIModel, AdminAISettingSource, AdminAISettings, UpdateAdminAISettingsRequest } from './types';

/** Select value for "no admin effort": the run inherits the environment's effort,
 *  or the model's default. Radix Select items cannot use ''. */
const EFFORT_INHERIT = 'inherit';

const SOURCE_LABEL: Record<AdminAISettingSource, string> = {
  admin: '관리자 설정',
  env: '환경변수',
  default: '기본값',
};

interface Choice {
  value: string;
  label: string;
}

/** The admin's pending edit; null until something is changed. */
interface SettingsDraft {
  model: string;
  effort: string;
  baseUrl: string;
}

interface SettingsDraftView {
  model: string;
  effort: string;
  baseUrl: string;
  dirty: boolean;
  request: UpdateAdminAISettingsRequest;
}

const supportedEfforts = (model: AdminAIModel | undefined): string[] =>
  model?.supported_reasoning_efforts.map((e) => e.reasoning_effort) ?? [];

/** Inheriting is only offered when the inherited environment effort fits the model;
 *  the server rejects the combination otherwise. An unlisted model cannot be checked. */
const canInherit = (model: AdminAIModel | undefined, envEffort: string): boolean =>
  !envEffort || !model || supportedEfforts(model).includes(envEffort);

/** Catalog models as "display name · id". A current model the catalog no longer
 *  lists stays selectable, marked, so the form never shows a blank value. */
const modelChoices = (models: AdminAIModel[], current: string): Choice[] => {
  const choices = models.map((m) => ({ value: m.id, label: `${m.display_name} · ${m.id}` }));
  if (current && !models.some((m) => m.id === current)) {
    choices.push({ value: current, label: `${current} (목록에 없음)` });
  }
  return choices;
};

/** The model's supported efforts after an inherit choice naming what it inherits.
 *  A current value that does not fit stays in the list, marked. */
const effortChoices = (models: AdminAIModel[], modelId: string, envEffort: string, current: string): Choice[] => {
  const model = models.find((m) => m.id === modelId);
  const supported = supportedEfforts(model);
  const inheritLabel = envEffort
    ? `환경변수 값 (${envEffort})`
    : model
      ? `모델 기본값 (${model.default_reasoning_effort})`
      : '모델 기본값';
  const choices: Choice[] = [];
  if (canInherit(model, envEffort)) {
    choices.push({ value: EFFORT_INHERIT, label: inheritLabel });
  } else if (current === EFFORT_INHERIT) {
    choices.push({ value: EFFORT_INHERIT, label: `${inheritLabel} (지원 안 됨)` });
  }
  choices.push(...supported.map((e) => ({ value: e, label: e })));
  if (current !== EFFORT_INHERIT && !supported.includes(current)) {
    choices.push({ value: current, label: `${current} (지원 안 됨)` });
  }
  return choices;
};

/** The effort choice to keep after switching models: unchanged when it still fits,
 *  otherwise inherit when that fits, otherwise the model's default named explicitly. */
const effortForModel = (models: AdminAIModel[], modelId: string, envEffort: string, effort: string): string => {
  const model = models.find((m) => m.id === modelId);
  const fits = effort === EFFORT_INHERIT ? canInherit(model, envEffort) : supportedEfforts(model).includes(effort);
  if (fits) return effort;
  if (canInherit(model, envEffort)) return EFFORT_INHERIT;
  return model?.default_reasoning_effort ?? EFFORT_INHERIT;
};

/** What the form shows and would save. Only what the admin set or changed goes in
 *  the request; an untouched env or default value is sent as '' so it keeps
 *  following the environment instead of being frozen as an admin choice. */
const settingsDraftView = (settings: AdminAISettings, draft: SettingsDraft | null): SettingsDraftView => {
  const adminModel = settings.source.model === 'admin' ? settings.model : '';
  const adminEffort = settings.source.reasoning_effort === 'admin' ? settings.reasoning_effort : '';
  const adminBaseUrl = settings.source.base_url === 'admin' ? settings.base_url : '';
  const model = draft?.model ?? settings.model;
  const effort = draft?.effort ?? (adminEffort || EFFORT_INHERIT);
  const baseUrl = draft?.baseUrl ?? settings.base_url;
  const request = {
    model: model !== settings.model || adminModel ? model : '',
    reasoning_effort: effort === EFFORT_INHERIT ? '' : effort,
    base_url: baseUrl !== settings.base_url || adminBaseUrl ? baseUrl : '',
  };
  return {
    model,
    effort,
    baseUrl,
    dirty:
      request.model !== adminModel ||
      request.reasoning_effort !== adminEffort ||
      request.base_url !== adminBaseUrl,
    request,
  };
};

/** One message per catalog failure: each needs a different fix. */
const catalogErrorMessage = (code: string): string => {
  if (code === 'runtime_unconfigured') return 'AI 런타임이 설정되지 않아 모델을 변경할 수 없습니다.';
  if (code === 'runtime_not_logged_in') return 'AI 런타임에 로그인되어 있지 않아 모델 목록을 가져올 수 없습니다.';
  return '모델 목록을 불러오지 못했습니다. 현재 설정은 유지되며 변경만 할 수 없습니다.';
};

export {
  EFFORT_INHERIT,
  SOURCE_LABEL,
  catalogErrorMessage,
  effortChoices,
  effortForModel,
  modelChoices,
  settingsDraftView,
};
export type { Choice, SettingsDraft, SettingsDraftView };
