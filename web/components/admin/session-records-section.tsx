'use client';

import { useId, useState, type ChangeEvent, type FormEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { useAuth } from '@/components/common/auth-context';
import { ADMIN_AI_KEY, ADMIN_AI_MODELS_KEY } from '@/components/admin/ai-account-section';
import {
  AIReportRequestError,
  fetchAdminAIModels,
  fetchAdminSessionRecordVocabulary,
  fetchProjects,
  mergeAdminSessionRecordVocabulary,
  setAdminSessionRecordVocabulary,
  SessionRecordRequestError,
  updateAdminSessionRecordSettings,
} from '@/lib/api';
import type { SessionRecordVocabulary } from '@/lib/session-analysis-types';
import type { AdminAIResponse, AdminAIModel, Project } from '@/lib/types';

const PANEL_CLASS = 'rounded-lg border border-border bg-surface px-4 py-4 space-y-3';
const INPUT_CLASS = 'h-8 min-w-0 rounded border border-border bg-surface px-2 text-[12px] text-ink disabled:cursor-not-allowed disabled:opacity-60';
const RUNTIME_OPTIONS = [
  { value: '', label: '주간 AI 런타임을 따름' },
  { value: 'openai-api', label: 'OpenAI API' },
  { value: 'claude-api', label: 'Claude API' },
  { value: 'litellm-api', label: 'LiteLLM API' },
] as const;

const numberText = (value: number): string => value.toLocaleString('en-US');
const parseNullableInteger = (value: string): number | null => value.trim() === '' ? null : Number(value);
const listFromText = (value: string): string[] => [...new Set(value.split(/[\n,]/).map((term) => term.trim()).filter(Boolean))];

const errorMessage = (error: unknown): string => error instanceof SessionRecordRequestError
  ? `서버 응답: ${error.code}`
  : error instanceof AIReportRequestError ? `AI 모델 목록을 읽지 못했습니다: ${error.code}`
    : '요청을 처리하지 못했습니다.';

interface VocabularyEditorProps {
  vocabulary: SessionRecordVocabulary;
  onSave: (components: string[], screens: string[]) => void;
  onMerge: (axis: 'component' | 'screen', from: string, to: string) => void;
  saving: boolean;
  merging: boolean;
  error: unknown;
}

const VocabularyEditor = ({ vocabulary, onSave, onMerge, saving, merging, error }: VocabularyEditorProps) => {
  const [componentsText, setComponentsText] = useState(vocabulary.components.join('\n'));
  const [screensText, setScreensText] = useState(vocabulary.screens.join('\n'));
  const [axis, setAxis] = useState<'component' | 'screen'>('component');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const handleComponentsChange = (event: ChangeEvent<HTMLTextAreaElement>) => setComponentsText(event.target.value);
  const handleScreensChange = (event: ChangeEvent<HTMLTextAreaElement>) => setScreensText(event.target.value);
  const handleAxisChange = (event: ChangeEvent<HTMLSelectElement>) => setAxis(event.target.value === 'screen' ? 'screen' : 'component');
  const handleFromChange = (event: ChangeEvent<HTMLInputElement>) => setFrom(event.target.value);
  const handleToChange = (event: ChangeEvent<HTMLInputElement>) => setTo(event.target.value);
  const handleSave = () => onSave(listFromText(componentsText), listFromText(screensText));
  const handleMerge = () => onMerge(axis, from.trim(), to.trim());

  return (
    <div className="space-y-3">
      <p className="text-[11px] text-ink-3">저장소 <code className="font-mono">{vocabulary.repository_key}</code>의 컴포넌트·화면 어휘입니다. 비우면 열린 어휘, 한 항목 이상 고정하면 닫힌 어휘로 적용됩니다.</p>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="space-y-1 text-[11px] text-ink-2">컴포넌트 어휘
          <textarea aria-label="컴포넌트 어휘" value={componentsText} onChange={handleComponentsChange} rows={5} className="w-full rounded border border-border bg-surface px-2 py-1.5 font-mono text-[11px] text-ink" />
          <span className="block text-[10px] text-ink-3">{vocabulary.components_closed ? '고정 어휘' : '열린 어휘'}</span>
        </label>
        <label className="space-y-1 text-[11px] text-ink-2">화면 어휘
          <textarea aria-label="화면 어휘" value={screensText} onChange={handleScreensChange} rows={5} className="w-full rounded border border-border bg-surface px-2 py-1.5 font-mono text-[11px] text-ink" />
          <span className="block text-[10px] text-ink-3">{vocabulary.screens_closed ? '고정 어휘' : '열린 어휘'}</span>
        </label>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <label className="space-y-1 text-[11px] text-ink-2">축
          <select aria-label="어휘 축" value={axis} onChange={handleAxisChange} className={INPUT_CLASS}>
            <option value="component">컴포넌트</option><option value="screen">화면</option>
          </select>
        </label>
        <label className="flex-1 space-y-1 text-[11px] text-ink-2">기존 항목
          <input aria-label="기존 항목" value={from} onChange={handleFromChange} className={`w-full ${INPUT_CLASS}`} />
        </label>
        <label className="flex-1 space-y-1 text-[11px] text-ink-2">새 항목
          <input aria-label="새 항목" value={to} onChange={handleToChange} className={`w-full ${INPUT_CLASS}`} />
        </label>
        <Button type="button" variant="outline" size="sm" disabled={!from.trim() || !to.trim() || merging} onClick={handleMerge}>항목 병합</Button>
        <Button type="button" size="sm" disabled={saving} onClick={handleSave}>어휘 저장</Button>
      </div>
      {error !== undefined && error !== null && <p role="alert" className="text-[11px] text-danger">{errorMessage(error)}</p>}
    </div>
  );
};

interface SessionRecordsAdminPanelProps {
  data: AdminAIResponse;
}

const SessionRecordsAdminPanel = ({ data }: SessionRecordsAdminPanelProps) => {
  const { isAdmin } = useAuth();
  const queryClient = useQueryClient();
  const records = data.records;
  const [repositoryKey, setRepositoryKey] = useState('');
  const [enabled, setEnabled] = useState(records?.settings?.enabled ?? records?.enabled ?? false);
  const [runtime, setRuntime] = useState(records?.settings?.runtime ?? '');
  const [dailyCap, setDailyCap] = useState(records?.settings?.daily_token_cap == null ? '' : String(records.settings.daily_token_cap));
  const [backfillEnabled, setBackfillEnabled] = useState(records?.settings?.backfill_enabled ?? false);
  const [backfillDays, setBackfillDays] = useState(records?.settings?.backfill_days == null ? '' : String(records.settings.backfill_days));
  const [backfillBudget, setBackfillBudget] = useState(records?.settings?.backfill_token_budget == null ? '' : String(records.settings.backfill_token_budget));
  const [model, setModel] = useState(records?.runtime_settings?.model ?? '');
  const [effort, setEffort] = useState(records?.runtime_settings?.reasoning_effort ?? '');
  const [settingsValidationError, setSettingsValidationError] = useState('');
  const repositoryInputId = useId();
  const repositoryListId = useId();
  const projects = useQuery({
    queryKey: ['session-record-admin-projects'],
    queryFn: () => fetchProjects(),
    enabled: isAdmin && !!records,
    staleTime: 60_000,
    retry: false,
  });
  const projectsByRepository = new Map<string, Project>();
  for (const project of projects.data ?? []) {
    const key = project.repository_id || project.project_hash;
    if (key && !projectsByRepository.has(key)) projectsByRepository.set(key, project);
  }
  const repositoryOptions = [...projectsByRepository.entries()];
  const vocabulary = useQuery({
    queryKey: ['admin-session-record-vocabulary', repositoryKey.trim()],
    queryFn: ({ signal }) => fetchAdminSessionRecordVocabulary(repositoryKey.trim(), signal),
    enabled: isAdmin && !!records && repositoryKey.trim() !== '',
    retry: false,
  });
  const catalogRuntime = runtime || records?.runtime || data.selected_runtime;
  const catalog = useQuery({
    queryKey: [...ADMIN_AI_MODELS_KEY, 'session-record', catalogRuntime],
    queryFn: () => fetchAdminAIModels(catalogRuntime),
    enabled: isAdmin && !!records && !!catalogRuntime,
    staleTime: 10 * 60_000,
    retry: false,
  });
  const settingsMutation = useMutation({
    mutationFn: updateAdminSessionRecordSettings,
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ADMIN_AI_KEY }); },
  });
  const vocabularyMutation = useMutation({
    mutationFn: (values: { components: string[]; screens: string[] }) => setAdminSessionRecordVocabulary(repositoryKey.trim(), values.components, values.screens),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ['admin-session-record-vocabulary', repositoryKey.trim()] }); },
  });
  const mergeMutation = useMutation({
    mutationFn: (values: { axis: 'component' | 'screen'; from: string; to: string }) =>
      mergeAdminSessionRecordVocabulary(repositoryKey.trim(), values.axis, values.from, values.to),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ['admin-session-record-vocabulary', repositoryKey.trim()] }); },
  });

  const handleEnabledChange = (event: ChangeEvent<HTMLInputElement>) => setEnabled(event.target.checked);
  const handleRuntimeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    setRuntime(event.target.value);
    setModel('');
    setEffort('');
  };
  const handleDailyCapChange = (event: ChangeEvent<HTMLInputElement>) => setDailyCap(event.target.value);
  const handleBackfillEnabledChange = (event: ChangeEvent<HTMLInputElement>) => setBackfillEnabled(event.target.checked);
  const handleBackfillDaysChange = (event: ChangeEvent<HTMLInputElement>) => setBackfillDays(event.target.value);
  const handleBackfillBudgetChange = (event: ChangeEvent<HTMLInputElement>) => setBackfillBudget(event.target.value);
  const handleModelChange = (event: ChangeEvent<HTMLSelectElement>) => { setModel(event.target.value); setEffort(''); };
  const handleEffortChange = (event: ChangeEvent<HTMLSelectElement>) => setEffort(event.target.value);
  const handleRepositoryChange = (event: ChangeEvent<HTMLInputElement>) => setRepositoryKey(event.target.value);
  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSettingsValidationError('');
    const daily = parseNullableInteger(dailyCap);
    const days = parseNullableInteger(backfillDays);
    const budget = parseNullableInteger(backfillBudget);
    if ([daily, days, budget].some((value) => value !== null && (!Number.isSafeInteger(value) || value < 0))) {
      setSettingsValidationError('한도와 기간은 0 이상의 정수 또는 빈 값이어야 합니다.');
      return;
    }
    settingsMutation.mutate({ enabled, runtime, daily_token_cap: daily, backfill_enabled: backfillEnabled,
      backfill_days: days, backfill_token_budget: budget, model, reasoning_effort: effort });
  };
  const handleSaveVocabulary = (components: string[], screens: string[]) => vocabularyMutation.mutate({ components, screens });
  const handleMergeVocabulary = (axis: 'component' | 'screen', from: string, to: string) => mergeMutation.mutate({ axis, from, to });

  if (!isAdmin) return <p className={PANEL_CLASS}>관리자 전용 설정입니다.</p>;
  if (!records) return <p className={PANEL_CLASS}>서버가 세션 기록 관리자 상태를 제공하지 않습니다.</p>;

  const savedSettings = records.settings;
  const settingsComplete = savedSettings !== undefined && records.runtime_settings !== undefined;
  const selectedModel = catalog.data?.models.find((item) => item.id === model);
  const modelCatalogError = catalog.isError ? errorMessage(catalog.error) : '';
  const canSave = settingsComplete && !(model && catalog.isError) && !settingsMutation.isPending;

  return (
    <div className="space-y-3">
      <section className={PANEL_CLASS}>
        <header className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h3 className="text-[13px] font-medium text-ink">세션 기록 생성 설정</h3>
            <p className="mt-0.5 text-[11px] text-ink-3">기록은 사용자가 현재 런타임과 고지에 동의한 본인 세션에서만 생성됩니다. UI는 PUT의 전체 설정을 그대로 보존해 보냅니다.</p>
          </div>
          <label className="flex items-center gap-2 text-[12px] text-ink">
            <input type="checkbox" role="switch" aria-label="세션 기록 생성" checked={enabled} onChange={handleEnabledChange} disabled={!settingsComplete || (!enabled && !records.runtime_ok)} className="accent-brand" />
            {enabled ? '켜짐' : '꺼짐'}
          </label>
        </header>
        {!settingsComplete && <p role="status" className="rounded bg-warning-soft px-2 py-1.5 text-[11px] text-warning-strong">현재 설정 전체를 안전하게 읽지 못해 저장을 막았습니다. 서버 GET 응답에 원본 runtime·한도·백필·model·reasoning_effort 설정이 필요합니다.</p>}
        {records.runtime_reason && <p role="status" className="text-[11px] text-warning-strong">런타임 상태: {records.runtime_reason}</p>}
        <form onSubmit={handleSubmit} className="grid gap-2 sm:grid-cols-2">
          <label className="space-y-1 text-[11px] text-ink-2">세션 기록 런타임
            <select aria-label="세션 기록 런타임" value={runtime} onChange={handleRuntimeChange} disabled={!settingsComplete} className={`w-full ${INPUT_CLASS}`}>
              {RUNTIME_OPTIONS.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
            </select>
          </label>
          <label className="space-y-1 text-[11px] text-ink-2">모델
            <select aria-label="세션 기록 모델" value={model} onChange={handleModelChange} disabled={!settingsComplete || catalog.isPending || !!modelCatalogError} className={`w-full ${INPUT_CLASS}`}>
              <option value="">모델을 선택하세요</option>
              {model && !(catalog.data?.models ?? []).some((item) => item.id === model) && <option value={model}>현재 저장값 · 목록에서 확인되지 않음</option>}
              {(catalog.data?.models ?? []).map((item: AdminAIModel) => <option key={item.id} value={item.id}>{item.display_name || item.id}</option>)}
            </select>
          </label>
          {modelCatalogError && <p role="alert" className="sm:col-span-2 text-[11px] text-danger">{modelCatalogError}</p>}
          {catalog.isPending && !!catalogRuntime && <p role="status" className="sm:col-span-2 text-[11px] text-ink-3">모델 목록을 읽는 중입니다.</p>}
          <label className="space-y-1 text-[11px] text-ink-2">Reasoning effort
            <select aria-label="세션 기록 reasoning effort" value={effort} onChange={handleEffortChange} disabled={!settingsComplete || !selectedModel} className={`w-full ${INPUT_CLASS}`}>
              <option value="">모델 기본값</option>
              {(selectedModel?.supported_reasoning_efforts ?? []).map((choice) => <option key={choice.reasoning_effort} value={choice.reasoning_effort}>{choice.reasoning_effort}</option>)}
            </select>
          </label>
          <label className="space-y-1 text-[11px] text-ink-2">일일 토큰 한도
            <input aria-label="일일 토큰 한도" type="number" min="0" value={dailyCap} onChange={handleDailyCapChange} disabled={!settingsComplete} className={`w-full ${INPUT_CLASS}`} />
          </label>
          <label className="flex items-center gap-2 text-[11px] text-ink-2">
            <input type="checkbox" checked={backfillEnabled} onChange={handleBackfillEnabledChange} disabled={!settingsComplete} className="accent-brand" /> 지난 세션 백필
          </label>
          <label className="space-y-1 text-[11px] text-ink-2">백필 일수
            <input aria-label="백필 일수" type="number" min="0" value={backfillDays} onChange={handleBackfillDaysChange} disabled={!settingsComplete || !backfillEnabled} className={`w-full ${INPUT_CLASS}`} />
          </label>
          <label className="space-y-1 text-[11px] text-ink-2">백필 토큰 예산
            <input aria-label="백필 토큰 예산" type="number" min="0" value={backfillBudget} onChange={handleBackfillBudgetChange} disabled={!settingsComplete || !backfillEnabled} className={`w-full ${INPUT_CLASS}`} />
          </label>
          <div className="sm:col-span-2 flex flex-wrap items-center justify-between gap-2">
            <p className="text-[10.5px] text-ink-3">빈 한도 값은 서버 기본값을 따릅니다.</p>
            <Button type="submit" size="sm" disabled={!canSave || (enabled && !records.runtime_ok)}>세션 기록 설정 저장</Button>
          </div>
        </form>
        {settingsValidationError && <p role="alert" className="text-[11px] text-danger">{settingsValidationError}</p>}
        {settingsMutation.error && <p role="alert" className="text-[11px] text-danger">{errorMessage(settingsMutation.error)}</p>}
      </section>

      <section className={PANEL_CLASS}>
        <h3 className="text-[13px] font-medium text-ink">생성·대기·실패 및 사용량</h3>
        <p className="text-[11px] text-ink-3">{records.today.day} ({records.today.tz}) · 현재 레코드 상태와 사용량은 집계 값입니다. 사용자별 세션 내용은 포함하지 않습니다.</p>
        <p className="text-[11px] tabular-nums text-ink-2">현재 런타임 동의 사용자 {numberText(records.consents.current)} / 활성 사용자 {numberText(records.consents.users)} · 작업자 {records.worker.state}{records.worker.paused_reason ? ` · ${records.worker.paused_reason}` : ''}</p>
        <p className="text-[12px] tabular-nums text-ink-2">토큰 사용 {numberText(records.today.used_tokens)} · 예약 {numberText(records.today.reserved_tokens)} · 미보고 호출 {numberText(records.today.unreported_calls)} · 한도 {numberText(records.today.cap_tokens)}</p>
        <p className="text-[11px] tabular-nums text-ink-3">확정 사용량: 입력 {numberText(records.today.input_tokens)} · 출력 {numberText(records.today.output_tokens)} · 추정 상한 토큰 {numberText(records.today.estimated_cap_tokens)}</p>
        <p className="text-[11px] text-ink-3">달러 비용은 API가 제공하는 요율·금액 정보가 없어 판단할 수 없습니다. 토큰 수를 금액으로 환산하지 않았습니다.</p>
        <p className="text-[12px] tabular-nums text-ink-2">대기 {numberText(records.backlog.due)} · 조용한 구간 {numberText(records.backlog.waiting_quiet)} · 백필 잔여 {numberText(records.backlog.backfill_remaining)} · 사용자 요청 {numberText(records.backlog.requested)}</p>
        <p className="text-[12px] tabular-nums text-ink-2">실패 세션 {numberText(records.failures.sessions)}</p>
        {Object.entries(records.failures.by_code).length > 0
          ? <ul aria-label="실패 사유" className="flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-danger">{Object.entries(records.failures.by_code).map(([code, count]) => <li key={code}><code>{code}</code> {numberText(count)}건</li>)}</ul>
          : <p className="text-[11px] text-ink-3">실패 코드 집계는 관측되지 않았습니다.</p>}
        {records.today.used_tokens + records.today.reserved_tokens >= records.today.cap_tokens && (
          <p role="status" className="text-[11px] text-warning-strong">보고된 사용량과 예약량의 합이 오늘 한도에 도달했습니다. (출처: 오늘 사용량·예약·한도 집계)</p>
        )}
      </section>

      <section className={PANEL_CLASS}>
        <h3 className="text-[13px] font-medium text-ink">저장소별 컴포넌트·화면 통제 어휘</h3>
        <label htmlFor={repositoryInputId} className="block text-[11px] text-ink-2">저장소 키</label>
        <input id={repositoryInputId} aria-label="저장소 키" list={repositoryListId} value={repositoryKey} onChange={handleRepositoryChange} placeholder="repository_id 또는 project_hash" className={`w-full ${INPUT_CLASS}`} />
        <datalist id={repositoryListId}>{repositoryOptions.map(([key, project]) => <option key={key} value={key}>{project.repository_name || project.project_name}</option>)}</datalist>
        {projects.isError && <p role="status" className="text-[10.5px] text-ink-3">저장소 목록을 읽지 못했습니다. 저장소 키를 직접 입력할 수 있습니다.</p>}
        {repositoryKey.trim() && vocabulary.isPending && <p role="status" className="text-[11px] text-ink-3">어휘를 읽는 중입니다.</p>}
        {vocabulary.isError && <p role="alert" className="text-[11px] text-danger">{errorMessage(vocabulary.error)}</p>}
        {vocabulary.data && (
          <VocabularyEditor
            key={`${vocabulary.data.repository_key}:${vocabulary.data.components.join('|')}:${vocabulary.data.screens.join('|')}`}
            vocabulary={vocabulary.data}
            onSave={handleSaveVocabulary}
            onMerge={handleMergeVocabulary}
            saving={vocabularyMutation.isPending}
            merging={mergeMutation.isPending}
            error={vocabularyMutation.error ?? mergeMutation.error}
          />
        )}
      </section>
    </div>
  );
};

export { SessionRecordsAdminPanel };
