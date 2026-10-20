'use client';

import { ClipboardList, FileText, FolderGit2, GitBranch, UserRound } from 'lucide-react';
import { formatBytes } from '@/lib/format';
import type {
  AnalysisHeader,
  AnalysisIdentities,
  AnalysisLive,
  DeepPartial,
  LiveInstance,
  LiveProcessMetrics,
} from '@/lib/session-analysis-types';
import { Chip, Hint, Unknown } from './atoms';
import {
  countsParts,
  formatClockNear,
  formatCount,
  formatDateTime,
  formatDuration,
  formatRelative,
  shareText,
  taskTypeLabel,
} from './format';
import { ContextStat, CostStat, TokensStat, ToolsStat } from './header-stats';
import { UNKNOWN_TEXT, waitingKindLabel } from './labels';

/**
 * "세부" 띠. 패스 A의 큰 지표 칸(토큰 구성, 비용 나눔, 컨텍스트 구성, 도구 호출과 서브에이전트)과 어디서·언제·태그·실시간·신원을
 * 한곳에 모았다. 머리의 칩이 요약이고 이 띠가 근거다 — 평소에는 접어 두고, `building`·`truncated` 같은 응답에서는 이것이
 * 본문이라 늘 펼쳐 둔다. 모르는 값은 0이 아니라 점선 "알 수 없음"이다.
 */

type Header = DeepPartial<AnalysisHeader>;

const LIVE_STATUS_LABEL: Record<string, string> = {
  busy: '진행 중',
  idle: '쉬는 중',
  waiting: '응답 대기',
  shell: '쉬는 중 (셸)',
  unknown: '판단 불가',
};

const MetaItem = ({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) => (
  <div className="flex items-baseline gap-1">
    <dt className="text-ink-3">
      {hint ? (
        <Hint label={hint} className="underline decoration-dotted underline-offset-2">
          {label}
        </Hint>
      ) : (
        label
      )}
    </dt>
    <dd className="tabular-nums text-ink-2">{children}</dd>
  </div>
);

const WhereLine = ({ header }: { header: Header }) => {
  const project = header.project?.name || header.project?.hash;
  const emails = header.accounts?.login_emails ?? [];
  const inferred = header.accounts?.inferred;
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-ink-2">
      {header.agent && <Chip tone={header.agent === 'claude' ? 'claude' : header.agent === 'codex' ? 'codex' : 'neutral'}>{header.agent}</Chip>}
      {header.model && (
        <Chip tone="outline" className="font-mono">
          {header.model}
        </Chip>
      )}
      {project && (
        <span className="inline-flex min-w-0 items-center gap-1">
          <FolderGit2 size={12} strokeWidth={1.75} className="shrink-0 text-ink-3" aria-label="프로젝트" role="img" />
          <span className="truncate">{project}</span>
        </span>
      )}
      {header.branch && (
        <span className="inline-flex min-w-0 items-center gap-1">
          <GitBranch size={12} strokeWidth={1.75} className="shrink-0 text-ink-3" aria-label="브랜치" role="img" />
          <span className="truncate font-mono">{header.branch}</span>
        </span>
      )}
      <span className="inline-flex min-w-0 flex-wrap items-center gap-1.5">
        <UserRound size={12} strokeWidth={1.75} className="shrink-0 text-ink-3" aria-label="계정" role="img" />
        {emails.length === 0 ? (
          <Unknown label="계정 미상" reason="이 세션은 어느 계정으로 쓰였는지 알려주는 기록이 없습니다." />
        ) : (
          <span className="truncate">{emails.join(', ')}</span>
        )}
        {inferred && (
          <Chip
            tone="warning"
            size="sm"
            title="관측된 값이 아닙니다. 이 세션은 계정을 알려주는 로그를 남기지 않아, 같은 사용자의 앞뒤 로그인 기록으로 추정한 계정입니다."
          >
            추정
          </Chip>
        )}
        {emails.length > 1 && (
          <Chip
            tone="warning"
            size="sm"
            title={`이 세션은 ${emails.length}개 계정에 걸쳐 있습니다. 토큰·비용은 계정을 가리지 않은 세션 전체 합계입니다.`}
          >
            계정 {emails.length}
          </Chip>
        )}
      </span>
    </div>
  );
};

/** 길이는 시작과 마지막 활동의 차이다. 서버가 주지 않았어도 두 시각을 알면 그 차이를 쓴다. */
const durationOf = (header: Header): number | null => {
  if (typeof header.duration_seconds === 'number') return header.duration_seconds;
  const start = header.started_at ? Date.parse(header.started_at) : NaN;
  const end = header.last_activity_at ? Date.parse(header.last_activity_at) : NaN;
  return Number.isNaN(start) || Number.isNaN(end) ? null : Math.max(0, Math.round((end - start) / 1000));
};

const TimeLine = ({ header, now, timeZone }: { header: Header; now: number; timeZone?: string }) => {
  const last = header.last_activity_at;
  const duration = durationOf(header);
  return (
    <dl className="flex flex-wrap items-baseline gap-x-3.5 gap-y-0.5 text-[11.5px]">
      <MetaItem label="시작">{header.started_at ? formatDateTime(header.started_at, timeZone) : <Unknown />}</MetaItem>
      <MetaItem label="마지막 활동">
        {last ? (
          <>
            {formatDateTime(last, timeZone)} <span className="text-ink-3">({formatRelative(last, now)})</span>
          </>
        ) : (
          <Unknown />
        )}
      </MetaItem>
      <MetaItem label="길이">{duration !== null ? formatDuration(duration) : <Unknown />}</MetaItem>
      <MetaItem label="활동 시간" hint="시작부터 마지막 활동까지의 길이와 달리, 기록이 이어진 시간만 더한 값입니다.">
        {header.active_seconds != null ? formatDuration(header.active_seconds) : <Unknown />}
      </MetaItem>
    </dl>
  );
};

const UNDETERMINABLE_BOUNDARY_REASON: Record<string, string> = {
  redacted: '비식별화로 사람 입력과 도구 결과를 가를 수 없어 요청·입력·턴을 세지 않았습니다. 0이 아닙니다.',
};

/** 요청·입력·턴이 판단 불가인 이유. 머리의 "요청" 칸과 이 띠가 같은 문장을 쓴다. */
const boundaryReason = (header: Header): string => {
  const boundaries = header.counts?.turn_boundaries;
  return (
    (boundaries?.reason ? UNDETERMINABLE_BOUNDARY_REASON[boundaries.reason] : undefined) ??
    (boundaries?.state === 'undeterminable' ? '턴의 경계를 판단할 수 없어 세지 않았습니다. 0이 아닙니다.' : '이 세션의 수를 아직 알 수 없습니다.')
  );
};

/** "요청 N · 입력 M · 턴 K". 턴 경계를 판단할 수 없으면 세 수는 0이 아니라 점선 "알 수 없음"이다. */
const CountsLine = ({ header }: { header: Header }) => {
  const parts = countsParts(header.counts);
  const reason = boundaryReason(header);
  return (
    <div role="group" aria-label="요청, 입력, 턴 수" className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[12px] text-ink-2">
      {parts.map((p, i) => (
        <span key={p.label} className="inline-flex items-center gap-1.5">
          {i > 0 && (
            <span aria-hidden="true" className="text-ink-4">
              ·
            </span>
          )}
          <span>{p.label}</span>
          {p.value === null ? <Unknown reason={reason} /> : <b className="font-semibold tabular-nums text-ink">{formatCount(p.value)}</b>}
        </span>
      ))}
    </div>
  );
};

/** cctrace가 파생한 태그(변경 영역, 작업 유형, 플랜 플래그). 세션 개요의 태그 줄(LLM 기록)과는 다른 값이다. */
const DerivedTags = ({ header }: { header: Header }) => {
  const area = header.tags?.change_area;
  const task = header.tags?.task_type;
  const flags = header.plan_flags;
  const any = area || task || flags?.has_plan_file || flags?.used_plan_mode;
  if (!any) return null;
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {area?.state === 'redacted' && (
        <Unknown label="변경 영역 가려짐" reason="도구 정책으로 편집한 경로가 가려져 변경 영역을 알 수 없습니다." />
      )}
      {area?.state === 'observed' && area.value && (
        <Chip tone="outline" title="편집을 가장 많이 한 폴더(저장소 루트 기준 앞 두 단계)">
          <span className="font-normal text-ink-3">변경 영역</span> <span className="font-mono">{area.value}</span>
        </Chip>
      )}
      {task?.value && (
        <Chip tone="outline" title="요청 문장을 키워드 규칙으로 분류한 추정값입니다.">
          <span className="font-normal text-ink-3">작업 유형</span> {taskTypeLabel(task.value)}{' '}
          <span className="font-normal text-ink-3 tabular-nums">· {shareText(task.share)} · 추정</span>
        </Chip>
      )}
      {flags?.has_plan_file && (
        <Chip tone="brand" icon={FileText} title="이 세션에 플랜 파일이 있습니다.">
          플랜 파일
        </Chip>
      )}
      {flags?.used_plan_mode && (
        <Chip tone="brand" icon={ClipboardList} title="이 세션에서 플랜 모드를 썼습니다. 플랜 파일이 없어도 쓸 수 있습니다.">
          플랜 모드
        </Chip>
      )}
    </div>
  );
};

/**
 * 칩에 보일 인스턴스. §8.4의 `alive`처럼 오래되지 않은 인스턴스 중 하트비트가 가장 최근인 것이다.
 * 모두 오래됐으면 그중 가장 최근 하나("오래된 생존 행")를 보인다. 첫 원소가 아니다: 인스턴스가 둘 이상이면 범주와
 * 다른 말을 하게 된다.
 */
const pickInstance = (instances: readonly LiveInstance[]): LiveInstance | undefined => {
  const alive = instances.filter((i) => !i.stale);
  const pool = alive.length > 0 ? alive : instances;
  return [...pool].sort((a, b) => Date.parse(b.heartbeat_at) - Date.parse(a.heartbeat_at))[0];
};

const IO_SOURCE_LABEL: Record<LiveProcessMetrics['io_source'], string> = {
  process_stream: '실제 프로세스 스트림에서 관측한 입출력량입니다.',
  transcript: '현재 저장된 사람 입력과 모델 출력 텍스트만 셉니다. 도구 결과와 가려진 본문은 제외하며, 실제 프로세스 입출력량은 아닙니다.',
  unknown: '입출력량의 수집 출처를 알 수 없어 실제 스트림인지 대화 기록량인지 판단할 수 없습니다.',
};

const IO_FAILURE_LABEL: Record<string, { label: string; detail: string }> = {
  input_relay_failed: { label: '입력 중계 실패', detail: '자식 프로세스에 입력을 전달하는 도중 중계가 끊겼습니다.' },
  output_relay_failed: { label: '출력 중계 실패', detail: '자식 프로세스 출력을 터미널로 전달하는 도중 중계가 끊겼습니다.' },
  upload_failed: { label: '업로드 실패', detail: '관측값 업로드가 실패해 마지막 확인 수치가 부분적일 수 있습니다.' },
  resize_failed: { label: '크기 전달 실패', detail: '터미널 크기 변경을 자식 프로세스에 전달하지 못했습니다.' },
  terminal_restore_failed: { label: '터미널 복구 실패', detail: '프로세스 종료 후 원래 터미널 상태를 복구하지 못했습니다.' },
};

const ProcessLine = ({ process, stale = false, now }: { process?: LiveProcessMetrics; stale?: boolean; now: number }) => {
  const source = process?.io_source ?? 'unknown';
  const scope = process?.io_scope ?? (source === 'transcript' ? 'session' : process?.pid != null ? 'instance' : 'unknown');
  const freshness = stale ? 'stale' : process?.io_freshness ?? (source === 'unknown' ? 'unknown' : 'fresh');
  const io = (value: number | null | undefined, unit: 'line' | 'byte') =>
    source !== 'unknown' && value != null && Number.isFinite(value) && value >= 0
      ? unit === 'byte' ? formatBytes(value) : formatCount(value)
      : <Unknown reason={IO_SOURCE_LABEL[source]} />;
  const pid = scope === 'instance' ? process?.pid : null;
  const hasFreshness = process?.io_as_of != null || (process?.io_freshness != null && process.io_freshness !== 'unknown');
  const failure = process?.io_failure_code ? IO_FAILURE_LABEL[process.io_failure_code] : undefined;
  const asOfHint = source === 'transcript'
    ? '현재 저장된 transcript에서 계산에 포함했거나 범위를 확인하지 못한 마지막 메시지 시각입니다.'
    : `관측 epoch ${process?.io_epoch ?? '미상'}`;
  const partialHint = source === 'transcript'
    ? '가려지거나 역할과 출처를 확인할 수 없는 저장 기록이 있어 해당 방향을 알 수 없음으로 표시했습니다.'
    : '중계가 일부 실패했습니다. 마지막 관측 시각을 확인하세요.';
  return (
    <dl role="group" aria-label="프로세스 상세" className="flex flex-wrap items-baseline gap-x-3.5 gap-y-0.5 text-[11px] text-ink-3">
      <MetaItem label="PID" hint="표시 중인 실행 인스턴스의 프로세스 ID입니다. 클라이언트가 전달하지 않으면 알 수 없습니다.">
        {pid != null ? formatCount(pid) : <Unknown reason={scope === 'session' ? '대화 기록량은 특정 프로세스 PID에 귀속되지 않습니다.' : '이 인스턴스의 PID를 수집하지 않았습니다.'} />}
      </MetaItem>
      <MetaItem label="입력 줄" hint={IO_SOURCE_LABEL[source]}>{io(process?.input_lines, 'line')}</MetaItem>
      <MetaItem label="출력 줄" hint={IO_SOURCE_LABEL[source]}>{io(process?.output_lines, 'line')}</MetaItem>
      <MetaItem label="입력 바이트" hint={IO_SOURCE_LABEL[source]}>{io(process?.input_bytes, 'byte')}</MetaItem>
      <MetaItem label="출력 바이트" hint={IO_SOURCE_LABEL[source]}>{io(process?.output_bytes, 'byte')}</MetaItem>
      <MetaItem label="입출력 출처">
        {source === 'process_stream' ? '실제 스트림' : source === 'transcript' ? '대화 기록량' : <Unknown reason={IO_SOURCE_LABEL.unknown} />}
      </MetaItem>
      {source !== 'unknown' && <MetaItem label="측정 범위">{scope === 'instance' ? '실행 인스턴스' : scope === 'session' ? '세션 합계' : <Unknown />}</MetaItem>}
      {process?.io_mode === 'pty' && source === 'process_stream' && <MetaItem label="출력 구성">PTY 합산 출력</MetaItem>}
      {process?.io_mode === 'pipes' && source === 'process_stream' && (
        <>
          <MetaItem label="stdout 줄" hint="분리 pipe 모드에서 자식 stdout에 전달된 줄 수입니다.">{io(process.stdout_lines, 'line')}</MetaItem>
          <MetaItem label="stdout 바이트" hint="분리 pipe 모드에서 자식 stdout에 전달된 바이트 수입니다.">{io(process.stdout_bytes, 'byte')}</MetaItem>
          <MetaItem label="stderr 줄" hint="분리 pipe 모드에서 자식 stderr에 전달된 줄 수입니다.">{io(process.stderr_lines, 'line')}</MetaItem>
          <MetaItem label="stderr 바이트" hint="분리 pipe 모드에서 자식 stderr에 전달된 바이트 수입니다.">{io(process.stderr_bytes, 'byte')}</MetaItem>
          <MetaItem label="출력 구성">stdout/stderr 분리</MetaItem>
        </>
      )}
      {process?.io_as_of && <MetaItem label={source === 'transcript' ? '마지막 기록' : '마지막 관측'} hint={asOfHint}>{formatRelative(process.io_as_of, now)}</MetaItem>}
      {process?.io_partial && (
        <MetaItem label="측정 상태">
          <Chip tone="warning" size="sm" title={failure?.detail ?? partialHint}>
            {failure ? `부분 측정 · ${failure.label}` : '부분 측정'}
          </Chip>
        </MetaItem>
      )}
      {!process?.io_partial && hasFreshness && freshness === 'stale' && <MetaItem label="측정 상태"><Chip tone="unknown" size="sm">오래된 측정</Chip></MetaItem>}
      {!process?.io_partial && hasFreshness && freshness === 'fresh' && <MetaItem label="측정 상태"><Chip tone="neutral" size="sm">{source === 'transcript' ? '저장 기록 기준' : '최신 측정'}</Chip></MetaItem>}
      {!process?.io_partial && hasFreshness && freshness === 'unknown' && source === 'transcript' && <MetaItem label="측정 상태"><Chip tone="unknown" size="sm">기록 범위 판단 불가</Chip></MetaItem>}
    </dl>
  );
};

/** 실시간(세션 주인에게만). 살아 있는 인스턴스와 수집기의 마지막 신호. */
const LiveLine = ({ live, now }: { live: AnalysisLive; now: number }) => {
  const first = pickInstance(live.instances);
  const extra = live.instances.length - 1;
  const collector = live.collector;
  const unknownStatusHint = first?.status === 'unknown'
    ? first.status_source === 'process_io_unknown'
      ? '실제 프로세스 입출력은 수집하지만 실행 중·유휴·응답 대기 상태는 관측하지 않아 판단할 수 없습니다.'
      : '이 인스턴스의 실행 상태 출처를 확인할 수 없어 판단 불가로 표시합니다.'
    : undefined;
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-ink-3">
      {first ? (
        <span className="inline-flex flex-wrap items-center gap-1.5">
          <span className="font-medium text-ink-2">실시간</span>
          {first.stale ? (
            <Chip tone="unknown" size="sm" title="상태가 오래돼 진행 중·응답 대기로 세지 않았습니다.">
              오래된 생존 행
            </Chip>
          ) : first.waiting_kind ? (
            // 상태가 busy여도 waiting_kind가 있으면 범주는 응답 대기다(§8.4). 칩이 범주와 다른 말을 하지 않게 한다.
            <Chip tone="warning" size="sm" title="이 인스턴스는 사용자의 응답을 기다리고 있습니다.">
              응답 대기 · {waitingKindLabel(first.waiting_kind)}
            </Chip>
          ) : (
            <Chip tone={first.status === 'unknown' ? 'unknown' : 'neutral'} size="sm" title={unknownStatusHint}>
              {LIVE_STATUS_LABEL[first.status] ?? first.status}
            </Chip>
          )}
          {first.name && <span className="font-mono">{first.name}</span>}
          <span>하트비트 {formatRelative(first.heartbeat_at, now)}</span>
          {extra > 0 && <span>· 인스턴스 +{extra}</span>}
        </span>
      ) : (
        <span>
          <span className="font-medium text-ink-2">실시간</span> 살아 있는 인스턴스 없음
        </span>
      )}
      <span className="inline-flex items-center gap-1.5">
        {collector.heartbeat_at || collector.last_stored_at ? (
          <>
            수집기 신호 {formatRelative(collector.heartbeat_at, now)}
            {collector.last_stored_at && <> · 마지막 저장 {formatRelative(collector.last_stored_at, now)}</>}
          </>
        ) : (
          <>
            수집기 신호 <Unknown reason="이 사용자의 수집기가 아직 신호를 보낸 적이 없습니다(관측 이전 클라이언트)." />
          </>
        )}
      </span>
    </div>
  );
};

/** 신원 값 목록을 키보드로도 읽히는 툴팁에 담는다. 비어 있으면 누를 것이 없다. */
const IdentityCount = ({ label, values }: { label: string; values: readonly string[] }) =>
  values.length === 0 ? (
    <span>{label} 0</span>
  ) : (
    <Hint
      label={
        <span className="block space-y-0.5">
          {values.map((value) => (
            <span key={value} className="block font-mono break-all">
              {value}
            </span>
          ))}
        </span>
      }
      ariaLabel={`${label} ${values.length}개: ${values.join(', ')}`}
      className="underline decoration-dotted underline-offset-2"
    >
      {label} {values.length}
    </Hint>
  );

const IdentitiesLine = ({ identities }: { identities: AnalysisIdentities }) => (
  <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-ink-3">
    <span className="font-medium text-ink-2">신원</span>
    <IdentityCount label="user_id" values={identities.user_ids} />
    <IdentityCount label="프로필" values={identities.profile_emails} />
    <IdentityCount label="계정" values={identities.login_emails} />
    {identities.mixed && (
      <Chip tone="warning" size="sm" title="여러 신원의 기록이 한 세션에 섞여 있습니다. 세션 주인에게는 보이지 않는 세션입니다.">
        신원 섞임
      </Chip>
    )}
  </div>
);

interface DetailsStripProps {
  id: string;
  /** 이 에이전트는 분석 대상이 아니다(unsupported). 요청 수, 컨텍스트, 도구는 "모름"이 아니라 해당 없음이라 그리지 않는다. */
  notApplicable?: boolean;
  header: Header | undefined;
  ownerView: boolean;
  now: number;
  timeZone?: string;
  live?: AnalysisLive;
  identities?: AnalysisIdentities;
  computedAt?: string;
  pendingSince?: string | null;
}

const DetailsStrip = ({ id, notApplicable = false, header, ownerView, now, timeZone, live, identities, computedAt, pendingSince }: DetailsStripProps) => {
  const h: Header = header ?? {};
  const transcriptIO = live?.transcript_io ?? live?.instances.find((instance) => instance.process?.io_source === 'transcript')?.process;
  const processInstances = live?.instances.filter((instance) => instance.process?.io_source !== 'transcript') ?? [];
  return (
    <div id={id} aria-label="세션 세부" role="group" className="@container space-y-2.5 border-b border-border bg-surface-2 px-3 py-2.5">
      <div className="grid grid-cols-2 gap-x-5 gap-y-3 @xl:grid-cols-4">
        <TokensStat header={h} />
        <CostStat header={h} />
        {!notApplicable && <ContextStat header={h} />}
        {!notApplicable && <ToolsStat header={h} />}
      </div>
      <div className="space-y-1.5 border-t border-border-subtle pt-2">
        <WhereLine header={h} />
        <TimeLine header={h} now={now} timeZone={timeZone} />
        {!notApplicable && (
          <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-1.5">
            <CountsLine header={h} />
            <DerivedTags header={h} />
          </div>
        )}
      </div>
      {(live || identities || computedAt || pendingSince) && (
        <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-1.5 border-t border-border-subtle pt-2">
          <div className="flex min-w-0 flex-wrap items-center gap-x-6 gap-y-1.5">
            {ownerView && live && <LiveLine live={live} now={now} />}
            {identities && <IdentitiesLine identities={identities} />}
          </div>
          {(computedAt || pendingSince) && (
            <p className="flex flex-wrap items-center gap-x-3 text-[10.5px] text-ink-3">
              {computedAt && <span>분석 계산 {formatClockNear(computedAt, now)}</span>}
              {pendingSince && (
                <span title="이 시각 이후의 새 기록은 아직 이 분석에 반영되지 않았습니다.">
                  새 기록 반영 대기 중 (
                  {formatClockNear(pendingSince, now) === UNKNOWN_TEXT ? '시각 모름' : `${formatClockNear(pendingSince, now)}부터`})
                </span>
              )}
            </p>
          )}
          {ownerView && live && (
            <div className="w-full space-y-1">
              {processInstances.map((instance) => (
                <ProcessLine
                  key={`${instance.process?.pid ?? 'unknown'}-${instance.heartbeat_at}`}
                  process={instance.process}
                  stale={instance.stale}
                  now={now}
                />
              ))}
              {transcriptIO && (
                <ProcessLine process={transcriptIO} now={now} />
              )}
              {processInstances.length === 0 && !transcriptIO && <ProcessLine now={now} />}
            </div>
          )}
        </div>
      )}
    </div>
  );
};

export { DetailsStrip, boundaryReason, durationOf };
