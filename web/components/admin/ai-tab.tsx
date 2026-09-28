'use client';

import { useState } from 'react';
import type { ChangeEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { CollectingLoader } from '@/components/common/collecting-loader';
import { useAuth } from '@/components/common/auth-context';
import { ADMIN_AI_KEY, ADMIN_AI_MODELS_KEY, AccountSection, OUTLINE_BUTTON_CLASS } from '@/components/admin/ai-account-section';
import { ProviderKeysSection } from '@/components/admin/ai-provider-keys-section';
import {
  AIReportRequestError,
  fetchAdminAI,
  fetchAdminAIModels,
  setAdminAIEnabled,
  setAdminAIRuntime,
  updateAdminAISchedule,
  updateAdminAISettings,
} from '@/lib/api';
// One array of weekday names for the whole product: the weekly screen prints
// the firing this panel sets, and the two must not drift apart.
import { WEEKDAY_NAMES } from '@/components/weekly/schedule-line';
import {
  RUNTIME_LABEL,
  enableBlockReason,
  enabledErrorMessage,
  runtimeErrorMessage,
  runtimeState,
  sharedAccountWarning,
} from '@/lib/admin-ai-runtime';
import {
  EFFORT_INHERIT,
  SOURCE_LABEL,
  catalogErrorMessage,
  effortChoices,
  effortForModel,
  modelChoices,
  settingsDraftView,
} from '@/lib/admin-ai-settings';
import type { Choice, SettingsDraft } from '@/lib/admin-ai-settings';
import { formatCompactNumber } from '@/lib/format';
import { cn } from '@/lib/utils';
import type {
  AdminAIModel,
  AdminAIResponse,
  AdminAIRuntime,
  AdminAISettingSource,
  AdminAISettings,
  AdminAIUsage,
  UpdateAdminAIScheduleRequest,
  UpdateAdminAISettingsRequest,
} from '@/lib/types';

// The server caches the catalog for 10 minutes; asking sooner only re-reads that cache.
const CATALOG_STALE_MS = 10 * 60_000;

const PANEL_CLASS = 'bg-surface border border-border rounded-lg px-4 py-4 space-y-3';
const SELECT_TRIGGER_CLASS =
  'w-full h-auto rounded-lg border-border bg-surface px-3 py-2 text-sm shadow-none focus-visible:ring-0 focus-visible:border-brand';

const CODEX_RUNTIME = 'codex-app-server';
const LITELLM_RUNTIME = 'litellm-api';

const AUTH_LABEL: Record<string, string> = {
  chatgpt: 'ChatGPT 로그인',
  api_key: 'API 키',
  none: '로그인 없음',
};

const SAVE_ERROR: Record<string, string> = {
  invalid_model: '모델 목록에 없는 모델입니다.',
  invalid_reasoning_effort: '적용될 모델이 지원하지 않는 reasoning effort 입니다.',
};

interface RuntimeOptionProps {
  runtime: AdminAIRuntime;
  checked: boolean;
  disabled: boolean;
  onSelect: (event: ChangeEvent<HTMLInputElement>) => void;
}

const RuntimeOption = ({ runtime, checked, disabled, onSelect }: RuntimeOptionProps) => {
  const state = runtimeState(runtime);
  const details = [
    runtime.auth_mode ? AUTH_LABEL[runtime.auth_mode] ?? runtime.auth_mode : '',
    runtime.account_email,
    runtime.plan_type,
    runtime.used_percent != null ? `사용량 ${Math.round(runtime.used_percent)}%` : '',
  ].filter(Boolean);
  return (
    <li>
      <label
        className={cn(
          'flex items-baseline justify-between gap-3 rounded-lg border px-3 py-2 text-[13px]',
          checked ? 'border-brand' : 'border-border',
          disabled ? 'cursor-not-allowed' : 'cursor-pointer hover:bg-canvas',
        )}
      >
        <span className={cn('flex items-center gap-2 shrink-0', runtime.configured ? 'text-ink' : 'text-ink-3')}>
          <input
            type="radio"
            name="ai-runtime"
            value={runtime.key}
            checked={checked}
            disabled={disabled}
            onChange={onSelect}
            className="accent-brand"
          />
          {RUNTIME_LABEL[runtime.key] ?? runtime.key}
          {checked && <span className="text-[11px] font-medium text-brand">사용 중</span>}
        </span>
        <span className="flex items-center gap-2 min-w-0 text-[12px] text-ink-2">
          <span className={cn('size-2 shrink-0 rounded-full', state.ok ? 'bg-success' : 'bg-border-strong')} />
          <span className="truncate">{[state.text, ...details].join(' · ')}</span>
        </span>
      </label>
    </li>
  );
};

const EnablePanel = ({ data }: { data: AdminAIResponse }) => {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: setAdminAIEnabled,
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ADMIN_AI_KEY }),
  });

  const blockReason = enableBlockReason(data);
  const errorMessage =
    mutation.error instanceof AIReportRequestError
      ? enabledErrorMessage(mutation.error.code)
      : mutation.error
        ? enabledErrorMessage('')
        : null;
  const envValue = data.env_enabled === null ? '' : data.env_enabled ? '켜짐' : '꺼짐';

  const handleToggle = (event: ChangeEvent<HTMLInputElement>) => mutation.mutate(event.target.checked);
  const handleClear = () => mutation.mutate(null);

  return (
    <section className={PANEL_CLASS}>
      <header className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-[13px] font-medium text-ink">AI 리포트 사용</h3>
          <p className="text-[11px] text-ink-3 mt-0.5">
            켜면 동의한 사용자의 작업 기록이 선택 런타임의 공급자로 전송됩니다. 꺼져 있으면 리포트 생성과 동의를 받지 않습니다.
          </p>
        </div>
        <label className="flex shrink-0 items-center gap-2 text-[13px] text-ink">
          <input
            type="checkbox"
            role="switch"
            aria-label="AI 리포트 사용"
            checked={data.enabled}
            // Turning on needs a runtime that can run; turning off never waits on one.
            disabled={mutation.isPending || (!data.enabled && blockReason !== null)}
            onChange={handleToggle}
            className="accent-brand"
          />
          {data.enabled ? '켜짐' : '꺼짐'}
        </label>
      </header>
      <footer className="flex items-center justify-between gap-2">
        <SourceNote source={data.enabled_source} envValue={envValue} />
        {data.enabled_source === 'admin' && (
          <Button variant="outline" onClick={handleClear} disabled={mutation.isPending} className={OUTLINE_BUTTON_CLASS}>
            관리자 설정 해제
          </Button>
        )}
      </footer>
      {!data.enabled && blockReason && <p className="text-[12px] text-ink-3">{blockReason}</p>}
      {errorMessage && <p className="text-[12px] text-danger">{errorMessage}</p>}
    </section>
  );
};

const SCHEDULE_HOURS = Array.from({ length: 24 }, (_, hour) => hour);
/** Minutes a firing may land on. Every minute would be a 60-item list for a
 *  choice nobody makes that finely. */
const SCHEDULE_MINUTES = [0, 10, 20, 30, 40, 50];

const pad = (n: number): string => String(n).padStart(2, '0');

const scheduleSelectClass =
  'rounded-[var(--r-sm)] border border-border bg-surface px-2 py-1 text-[13px] text-ink disabled:opacity-50';

/** The weekly firing every user follows until they set their own.
 *
 *  The weekday and time stay visible while it is off, because an administrator
 *  has to see what turning it on would do before turning it on. */
const SchedulePanel = ({ data }: { data: AdminAIResponse }) => {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: updateAdminAISchedule,
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ADMIN_AI_KEY }),
  });
  const { schedule } = data;

  // The save replaces the whole row, so every control sends the state the row
  // should end up with -- not just the field it owns. Sending one field cleared
  // the other three: turning the switch on and then choosing a weekday put the
  // switch back to "no admin word", and the administrator could never have both.
  const whole = (over: UpdateAdminAIScheduleRequest): UpdateAdminAIScheduleRequest => ({
    enabled: schedule.enabled,
    weekday: schedule.weekday,
    hour: schedule.hour,
    minute: schedule.minute,
    ...over,
  });

  const handleToggle = (event: ChangeEvent<HTMLInputElement>) => mutation.mutate(whole({ enabled: event.target.checked }));
  const handleWeekday = (event: ChangeEvent<HTMLSelectElement>) => mutation.mutate(whole({ weekday: Number(event.target.value) }));
  const handleHour = (event: ChangeEvent<HTMLSelectElement>) => mutation.mutate(whole({ hour: Number(event.target.value) }));
  const handleMinute = (event: ChangeEvent<HTMLSelectElement>) => mutation.mutate(whole({ minute: Number(event.target.value) }));
  // The one request that is not the current state: clearing hands every field
  // back to the built-in default.
  const handleClear = () => mutation.mutate({ enabled: null, weekday: null, hour: null, minute: null });

  return (
    <section className={PANEL_CLASS}>
      <header className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-[13px] font-medium text-ink">주간 자동 생성</h3>
          <p className="text-[11px] text-ink-3 mt-0.5">
            지난 주 리포트를 지정한 요일·시각에 자동으로 만듭니다. 시각은 사용자 각자의 시간대로 읽히고, 사용자가 자기 화면에서 바꿀 수 있습니다.
          </p>
        </div>
        <label className="flex shrink-0 items-center gap-2 text-[13px] text-ink">
          <input
            type="checkbox"
            role="switch"
            aria-label="주간 자동 생성"
            checked={schedule.enabled}
            disabled={mutation.isPending}
            onChange={handleToggle}
            className="accent-brand"
          />
          {schedule.enabled ? '켜짐' : '꺼짐'}
        </label>
      </header>
      <div className="flex flex-wrap items-center gap-2">
        <select className={scheduleSelectClass} value={schedule.weekday} onChange={handleWeekday} disabled={mutation.isPending} aria-label="요일">
          {WEEKDAY_NAMES.map((name, index) => (
            <option key={name} value={index}>
              {name}요일
            </option>
          ))}
        </select>
        <select className={scheduleSelectClass} value={schedule.hour} onChange={handleHour} disabled={mutation.isPending} aria-label="시">
          {SCHEDULE_HOURS.map((value) => (
            <option key={value} value={value}>
              {pad(value)}시
            </option>
          ))}
        </select>
        <select className={scheduleSelectClass} value={schedule.minute} onChange={handleMinute} disabled={mutation.isPending} aria-label="분">
          {SCHEDULE_MINUTES.map((value) => (
            <option key={value} value={value}>
              {pad(value)}분
            </option>
          ))}
        </select>
        <p className="text-[12px] tabular-nums text-ink-3">
          매주 {WEEKDAY_NAMES[schedule.weekday]}요일 {pad(schedule.hour)}:{pad(schedule.minute)}
          {/* A user who saved no zone fires in the zone of their last report; only
              one with neither falls back to the server's default. */}
          {` · 시간대를 정하지 않았고 리포트도 없는 사용자는 ${schedule.tz || 'UTC'} 기준`}
        </p>
      </div>
      <footer className="flex items-center justify-between gap-2">
        <SourceNote source={schedule.when_source === 'admin' ? 'admin' : 'default'} envValue="" />
        {(schedule.when_source === 'admin' || schedule.enabled_source === 'admin') && (
          <Button variant="outline" onClick={handleClear} disabled={mutation.isPending} className={OUTLINE_BUTTON_CLASS}>
            관리자 설정 해제
          </Button>
        )}
      </footer>
      {mutation.error && <p className="text-[12px] text-danger">저장하지 못했습니다. 다시 시도해 주세요.</p>}
    </section>
  );
};

const RuntimePanel = ({ data }: { data: AdminAIResponse }) => {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: setAdminAIRuntime,
    // Status, settings and the catalog all belong to the selected runtime.
    onSuccess: () => {
      for (const queryKey of [ADMIN_AI_KEY, ADMIN_AI_MODELS_KEY]) {
        void queryClient.invalidateQueries({ queryKey });
      }
    },
  });

  const selected = data.runtimes.find((rt) => rt.key === data.selected_runtime);
  const warning = sharedAccountWarning(selected);
  const errorMessage =
    mutation.error instanceof AIReportRequestError
      ? runtimeErrorMessage(mutation.error.code, mutation.error.detail)
      : mutation.error
        ? runtimeErrorMessage('', '')
        : null;

  const handleSelect = (event: ChangeEvent<HTMLInputElement>) => mutation.mutate(event.target.value);
  const handleClear = () => mutation.mutate('');

  return (
    <section className={PANEL_CLASS}>
      <header>
        <h3 className="text-[13px] font-medium text-ink">런타임</h3>
        <p className="text-[11px] text-ink-3 mt-0.5">
          다음 리포트 생성부터 적용됩니다. 설정되지 않은 런타임은 선택할 수 없고, 선택하지 않으면 어떤 런타임도 쓰지 않습니다.
        </p>
      </header>
      <ul className="space-y-2">
        {/* A runtime this build cannot drive is not offered: the server refuses
            the choice anyway, so showing the row would only invite a click that
            comes back as an error. */}
        {data.runtimes.filter((rt) => rt.implemented).map((rt) => (
          <RuntimeOption
            key={rt.key}
            runtime={rt}
            checked={rt.key === data.selected_runtime}
            disabled={!rt.configured || mutation.isPending}
            onSelect={handleSelect}
          />
        ))}
      </ul>
      <footer className="flex items-center justify-between gap-2">
        <SourceNote source={data.runtime_source} envValue={data.env_runtime} />
        {data.runtime_source === 'admin' && (
          <Button variant="outline" onClick={handleClear} disabled={mutation.isPending} className={OUTLINE_BUTTON_CLASS}>
            관리자 선택 해제
          </Button>
        )}
      </footer>
      {data.selection_reason_code === 'runtime_unavailable' && data.selection_reason && (
        <p className="rounded-lg bg-danger-soft px-3 py-2 text-[12px] text-danger-strong">{data.selection_reason}</p>
      )}
      {errorMessage && <p className="text-[12px] text-danger">{errorMessage}</p>}
      {warning && <p className="rounded-lg bg-warning-soft px-3 py-2 text-[12px] text-warning-strong">{warning}</p>}
      {/* Only a configured Codex runtime has an account to manage. */}
      {selected?.key === CODEX_RUNTIME && selected.configured && <AccountSection authLabel={AUTH_LABEL} />}
    </section>
  );
};

const SourceNote = ({ source, envValue }: { source: AdminAISettingSource; envValue: string }) => (
  <p className="text-[11px] text-ink-3">
    적용 출처: <span className="font-medium text-ink-2">{SOURCE_LABEL[source]}</span>
    {envValue && source !== 'env' && <span> · 환경변수 값 {envValue}</span>}
  </p>
);

interface SettingFieldProps {
  label: string;
  value: string;
  choices: Choice[];
  disabled: boolean;
  source: AdminAISettingSource;
  envValue: string;
  onChange: (value: string) => void;
}

const SettingField = ({ label, value, choices, disabled, source, envValue, onChange }: SettingFieldProps) => (
  <div className="space-y-1">
    <span className="block text-[11px] text-ink-2">{label}</span>
    <Select value={value} onValueChange={onChange} disabled={disabled}>
      <SelectTrigger aria-label={label} className={SELECT_TRIGGER_CLASS}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {choices.map((c) => (
          <SelectItem key={c.value} value={c.value}>
            {c.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
    <SourceNote source={source} envValue={envValue} />
  </div>
);

interface SettingsPanelProps {
  runtime: string;
  settings: AdminAISettings;
  models: AdminAIModel[] | undefined;
  catalogLoading: boolean;
  catalogError: string | null;
}

const SettingsPanel = ({ runtime, settings, models, catalogLoading, catalogError }: SettingsPanelProps) => {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<SettingsDraft | null>(null);
  const view = settingsDraftView(settings, draft);
  const envEffort = settings.env_reasoning_effort;

  const mutation = useMutation({
    mutationFn: (req: UpdateAdminAISettingsRequest) => updateAdminAISettings(req, runtime),
    onSuccess: (res) => {
      // A save that lands after the runtime changed must not overwrite the new runtime's settings.
      queryClient.setQueryData<AdminAIResponse>(ADMIN_AI_KEY, (prev) =>
        prev && prev.selected_runtime === res.settings.runtime
          ? { ...prev, model: res.settings.model, settings: res.settings }
          : prev,
      );
      setDraft(null);
    },
  });

  // Without a catalog the current values stay visible as the only choice; the
  // selects are disabled, so nothing unchecked can be picked.
  const modelOptions = models ? modelChoices(models, view.model) : [{ value: view.model, label: view.model }];
  const effortOptions = models
    ? effortChoices(models, view.model, envEffort, view.effort)
    : [{ value: view.effort, label: view.effort === EFFORT_INHERIT ? settings.reasoning_effort || '모델 기본값' : view.effort }];
  const locked = !models || mutation.isPending;
  const hasOverride =
    settings.source.model === 'admin' ||
    settings.source.reasoning_effort === 'admin' ||
    settings.source.base_url === 'admin';
  const saveError =
    mutation.error instanceof AIReportRequestError
      ? SAVE_ERROR[mutation.error.code] ?? catalogErrorMessage(mutation.error.code)
      : mutation.error
        ? '저장하지 못했습니다. 다시 시도해 주세요.'
        : null;

  const handleModelChange = (model: string) => {
    mutation.reset();
    setDraft({ model, effort: effortForModel(models ?? [], model, envEffort, view.effort), baseUrl: view.baseUrl });
  };
  const handleEffortChange = (effort: string) => {
    mutation.reset();
    setDraft({ model: view.model, effort, baseUrl: view.baseUrl });
  };
  const handleBaseUrlChange = (event: ChangeEvent<HTMLInputElement>) => {
    mutation.reset();
    setDraft({ model: view.model, effort: view.effort, baseUrl: event.target.value });
  };
  const handleSave = () => mutation.mutate(view.request);
  // Clearing needs no catalog, so it stays available while the catalog is down.
  const handleReset = () => mutation.mutate({ model: '', reasoning_effort: '', base_url: '' });

  return (
    <section className={PANEL_CLASS}>
      <header>
        <h3 className="text-[13px] font-medium text-ink">분석 모델</h3>
        <p className="text-[11px] text-ink-3 mt-0.5">다음 리포트 생성부터 적용됩니다. 관리자 설정 → 환경변수 → 기본값 순으로 적용합니다.</p>
      </header>

      {catalogLoading && <CollectingLoader className="py-2" />}
      {catalogError && (
        <p className="rounded-lg bg-danger-soft px-3 py-2 text-[12px] text-danger-strong">{catalogError}</p>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <SettingField
          label="모델"
          value={view.model}
          choices={modelOptions}
          disabled={locked}
          source={settings.source.model}
          envValue={settings.env_model}
          onChange={handleModelChange}
        />
        <SettingField
          label="Reasoning effort"
          value={view.effort}
          choices={effortOptions}
          disabled={locked}
          source={settings.source.reasoning_effort}
          envValue={envEffort}
          onChange={handleEffortChange}
        />
      </div>

      {runtime === LITELLM_RUNTIME && (
        <label className="grid gap-1">
          <span className="text-[12px] text-ink-2">런타임 주소</span>
          <input
            type="url"
            value={view.baseUrl}
            onChange={handleBaseUrlChange}
            disabled={mutation.isPending}
            placeholder="https://litellm.example.com"
            className="h-9 rounded-lg border border-border bg-canvas px-3 text-[13px] text-ink placeholder:text-ink-3 disabled:opacity-50"
          />
          <span className="text-[11px] text-ink-3">
            {settings.source.base_url === 'env' && settings.env_base_url
              ? `환경변수 값을 따르는 중: ${settings.env_base_url}`
              : '자체 호스팅 프록시 주소입니다. 끝의 /v1 은 붙여도 됩니다.'}
          </span>
        </label>
      )}

      {saveError && <p className="text-xs text-danger">{saveError}</p>}

      <footer className="flex justify-end gap-2">
        <Button
          variant="outline"
          onClick={handleReset}
          disabled={!hasOverride || mutation.isPending}
          className="h-auto py-2 px-4 text-sm font-medium rounded-lg border-border text-ink-2 shadow-none hover:bg-canvas"
        >
          기본값으로 되돌리기
        </Button>
        <Button
          onClick={handleSave}
          disabled={locked || !view.dirty}
          className="h-auto py-2 px-4 text-sm font-semibold rounded-lg text-white bg-brand hover:bg-brand-hover disabled:opacity-30 disabled:cursor-not-allowed"
        >
          저장
        </Button>
      </footer>
    </section>
  );
};

const UsagePanel = ({ usage }: { usage: AdminAIUsage }) => (
  <section className={PANEL_CLASS}>
    <h3 className="text-[13px] font-medium text-ink">이번 주 사용량 (UTC)</h3>
    <p className="text-[13px] text-ink-2 tabular-nums">
      생성 {usage.runs}회 · 실패 {usage.failed}회 · 입력 {formatCompactNumber(usage.input_tokens)} / 출력{' '}
      {formatCompactNumber(usage.output_tokens)}
    </p>
    <p className="text-[11px] text-ink-3">집계 수치만 표시합니다. 리포트 내용은 열람할 수 없습니다.</p>
  </section>
);

const AITab = () => {
  const { isAdmin } = useAuth();
  // No refetchInterval: status is read when the tab opens, and settings change
  // only through this form, whose save writes the cache directly.
  const { data, isLoading, isError } = useQuery<AdminAIResponse>({
    queryKey: ADMIN_AI_KEY,
    queryFn: fetchAdminAI,
    enabled: isAdmin,
  });
  const runtime = data?.selected_runtime ?? '';
  // Keyed by runtime so model IDs of different runtimes never mix.
  const catalog = useQuery({
    queryKey: [...ADMIN_AI_MODELS_KEY, runtime],
    queryFn: () => fetchAdminAIModels(runtime),
    enabled: isAdmin && runtime !== '',
    staleTime: CATALOG_STALE_MS,
    // Unconfigured and logged-out answer the same on every retry.
    retry: false,
  });

  if (!isAdmin) {
    return <div className="px-4 py-6 text-sm text-ink-3">관리자 전용 페이지입니다.</div>;
  }

  const catalogError = catalog.isError
    ? catalogErrorMessage(catalog.error instanceof AIReportRequestError ? catalog.error.code : '')
    : null;
  // codex-app-server has no credential: its account is the account section.
  const credentials = data?.runtimes.flatMap((rt) => (rt.credential ? [rt.credential] : [])) ?? [];

  return (
    <div className="space-y-4">
      <header>
        <h2 className="text-[16px] font-semibold text-ink">AI</h2>
        <p className="text-[13px] text-ink-3 mt-0.5">주간 AI 리포트 사용 여부, 런타임 선택, API 키, 분석 모델, 사용량</p>
      </header>

      {isLoading && <CollectingLoader className="py-6" />}
      {isError && (
        <div className="bg-danger-soft border border-danger/40 rounded-lg px-4 py-6 text-sm text-danger-strong">
          AI 설정을 불러오지 못했습니다. 다시 시도해 주세요.
        </div>
      )}
      {data && (
        <>
          <EnablePanel data={data} />
          <SchedulePanel data={data} />
          <RuntimePanel data={data} />
          {data.secrets_reason && (
            <p className="rounded-lg bg-warning-soft px-3 py-2 text-[12px] text-warning-strong">{data.secrets_reason}</p>
          )}
          <ProviderKeysSection credentials={credentials} />
          {data.settings ? (
            <SettingsPanel
              // A new runtime starts from its own settings, not the previous draft.
              key={data.selected_runtime}
              runtime={data.selected_runtime}
              settings={data.settings}
              models={catalog.data?.models}
              catalogLoading={catalog.isLoading}
              catalogError={catalogError}
            />
          ) : (
            <section className={cn(PANEL_CLASS, 'text-sm text-ink-3')}>런타임을 선택하면 분석 모델을 설정할 수 있습니다.</section>
          )}
          <UsagePanel usage={data.usage_this_week} />
        </>
      )}
    </div>
  );
};

export { AITab };
