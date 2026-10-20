/**
 * 세션 상세 분석 API의 응답 형태.
 *
 * 계약의 원문은 docs/planning/session-detail-analysis/p1b-design.md 다.
 *   §8.2  GET /api/sessions/{session_id}/analysis 응답과 턴 객체
 *   §8.3  GET /api/session-overview?include=analysis 의 행별 `analysis`
 *   §8.4  관측 범주 키
 *   §3.9  입력 종류, §3.12 신호 상태, §3.18 제목 상태, §4.4 비용 출처
 *
 * 필드 이름은 계약 그대로 쓴다(snake_case). 열거된 문자열은 유니온으로 닫았다.
 * 계약이 값 집합을 적지 않은 필드(`mode`, `agent`, `task_type.value`)는 string 으로 둔다.
 *
 * 숫자 필드의 `null`은 "이 세션은 그 값을 모른다"는 뜻이다. 0이 아니다. 화면은 둘을 다르게 그린다.
 * 선택(`?`) 필드는 "그 상태에서는 응답에 키가 없다"는 뜻이다(예: 비소유 관리자에게는 `signals`가 없다).
 *
 * 계약이 모양을 정하지 않은 곳은 각 필드의 주석에 "계약 미정"으로 적었다. P2가 실제 응답을 받아
 * 맞춘다.
 *
 * 패스 C(2026-10-07)가 레퍼런스의 나머지 요소를 위해 계약을 넓혔다: 판정(`judgement`), 세션 기록(`record`), 커밋(`commits`),
 * task(`tasks`), 메모와 분류, 턴의 커밋·task·작업 항목, 목록 행의 요약 필드, 타임라인 메시지, 전역 프레임. 파일 맨 아래의
 * "패스 C 확장"에 모여 있고, 응답 쪽 필드는 통합 패스 전까지 선택(`?`)이다.
 */

/* ============================================================
   열거
   ============================================================ */

/** 응답의 `state` (§7.3). */
export type AnalysisState = 'ready' | 'stale' | 'building' | 'none' | 'truncated' | 'unsupported';

/** 목록 행의 `analysis.state`. 응답 상태에 `withheld`(혼합 신원 세션, 목록에서만)가 더해진다 (§7.3, §8.3). */
export type SessionRowAnalysisState = AnalysisState | 'withheld';

export type TitleSource = 'custom' | 'ai' | 'agent_name' | 'first_request' | 'none';
export type TitleState = 'ok' | 'withheld' | 'redacted' | 'none';
export type NameSource = 'user' | 'derived' | 'auto' | 'custom_title';

/** 관측 범주 키 (§8.2, §8.4). 판정 순서는 계획 §5.3. */
export type SessionCategoryKey =
  | 'waiting'
  | 'running'
  | 'ended'
  | 'stale_live'
  | 'open_question'
  | 'idle'
  | 'collector_delayed'
  | 'recent'
  | 'inactive';

/** 실시간 상태의 waitingFor 종류 (계획 §4.1(3)). */
export type WaitingKind = 'input' | 'permission' | 'dialog' | 'sandbox' | 'worker' | 'goal' | 'other';

/** 세션 비용 출처 (§4.4). */
export type CostSource = 'otel' | 'otel_partial' | 'estimate' | 'estimate_pending' | 'none';
/** 턴 비용 출처 (§4.4). */
export type TurnCostSource =
  | 'otel'
  | 'otel_partial'
  | 'expired'
  | 'estimate'
  | 'estimate_pending'
  | 'no_rate'
  | 'unattributable'
  | 'none';
/** `unattributable`의 이유 (§4.4). */
export type TurnCostReason = 'pre_p0' | 'shared_prompt_id';
/** OTEL이 모두 만료된 세션의 비용 주석 (§4.4). */
export type CostNote = 'otel_expired';

/** 컨텍스트 창 출처 (§3.11). */
export type ContextSource = 'observed' | 'model_default' | 'unknown';

/** 턴 상태 (§3.6, §3.8). */
export type TurnStatus = 'ended' | 'interrupted' | 'error' | 'no_response' | 'open' | 'undeterminable';
/** `undeterminable` 턴의 이유 (§3.6, §3.8). */
export type TurnStatusReason = 'pre_collect' | 'redacted' | 'no_lifecycle' | 'no_end_line';
export type TurnBranch = 'active' | 'abandoned';

/** 입력 출처 (§3.9 `input_kind`). */
export type InputKind =
  | 'typed'
  | 'queued'
  | 'answer'
  | 'command'
  | 'notification'
  | 'peer'
  | 'channel'
  | 'auto'
  | 'scheduled'
  | 'sdk'
  | 'system'
  | 'unknown';

export type PreviewState = 'ok' | 'redacted' | 'withheld' | 'none';
export type QuestionStatus = 'answered' | 'rejected' | 'unanswered';
export type PlanDecision = 'approved' | 'rejected' | 'interrupted' | 'pending';
/** 질문·플랜 본문의 상태. */
export type BodyState = 'observed' | 'redacted' | 'withheld';

export type RecapState = 'observed' | 'withheld' | 'redacted' | 'pre_collect' | 'none';
export type RecapAbsentReason =
  | 'too_few_turns'
  | 'headless'
  | 'not_away'
  | 'pre_collect'
  | 'withheld'
  | 'redacted';

export type CopiesState = 'judged' | 'skipped_large_group';
/** 실시간 인스턴스 상태 (§8.4). */
export type LiveStatus = 'busy' | 'idle' | 'waiting' | 'shell' | 'unknown';

/** 신호가 `undeterminable`인 이유 (§3.12). */
export type SignalUndeterminableReason =
  | 'pre_lineage'
  | 'pre_collect'
  | 'withheld'
  | 'dropped'
  | 'redacted'
  | 'no_list'
  | 'no_git';

/** 목록 정렬 (`sort=` 쿼리 값, §8.3). */
export type SessionSort = 'start' | 'activity' | 'cost' | 'requests';
/** 플랜 필터 (`plan=` 쿼리 값, §8.3). 파일과 모드는 서로 다른 사실이다 (계획 B6). */
export type PlanFilter = 'file' | 'mode';

/* ============================================================
   머리 (header)
   ============================================================ */

export interface AnalysisTitle {
  /** `redacted`·`none`이면 null. `withheld`면 첫 요청 미리보기 문장이다. */
  text: string | null;
  source: TitleSource;
  state: TitleState;
}

export interface AnalysisName {
  text: string;
  source: NameSource;
}

export interface SessionCategory {
  key: SessionCategoryKey;
  /** `waiting`일 때 무엇을 기다리는지. 없으면 생략·null·빈 문자열. */
  waiting_kind?: WaitingKind | '' | null;
  as_of: string;
}

export interface AnalysisProject {
  hash: string;
  name: string | null;
}

export interface AnalysisAccounts {
  login_emails: string[];
  /** 로그인 기록으로 추정한 계정이면 true (관측값이 아니다). */
  inferred: boolean;
}

export interface TurnBoundaries {
  state: 'observed' | 'undeterminable';
  /** 비식별화 또는 구형 기록의 계보 키 부재(p1b-design §3.5-3). */
  reason: 'redacted' | 'pre_lineage' | null;
}

export interface AnalysisCounts {
  /** 판단 불가(`turn_boundaries.state = undeterminable`)면 requests·inputs·turns·abandoned_turns는 null이다. */
  requests: number | null;
  inputs: number | null;
  turns: number | null;
  abandoned_turns: number | null;
  /** API 호출 수. 사람 요청 수와 다르다. */
  api_requests: number;
  turn_boundaries: TurnBoundaries;
}

export interface TokenBreakdown {
  input: number;
  output: number;
  cache_read: number;
  cache_create: number;
}

export interface AnalysisTokens extends TokenBreakdown {
  /**
   * 계약(§8.2)의 값은 `request_set` 하나다. 계약 미정: `truncated`의 합계는 롤업 행에서 오는데(§3.1) 그때의
   * basis 값이 정해져 있지 않아 `rollup`으로 가정했다. 모르는 값이 와도 툴팁은 "롤업 합계"로 쓴다.
   */
  basis: 'request_set' | 'rollup';
}

export interface AnalysisCost {
  usd: number | null;
  source: CostSource;
  /** 턴에 붙은 비용의 합. */
  turn_attached_usd: number | null;
  /** 턴에 붙지 않은 호출(제목 생성, 압축, P0 이전 턴, 나눌 수 없는 명령 턴 등)의 비용. */
  unattached_turn_usd: number | null;
  note: CostNote | null;
}

export interface ContextMix {
  input: number;
  cache_read: number;
  cache_create: number;
}

export interface AnalysisContext {
  tokens: number | null;
  /** 창 크기를 모르면 null (이때 percent도 null). */
  window: number | null;
  /** 0~100. 계약은 단위를 적지 않았다(§8.2의 `percent: N`). 퍼센트로 읽는다. */
  percent: number | null;
  source: ContextSource;
  /** 세션 어딘가에서 큰 창(1M)을 쓴 흔적이 있다 (§3.11). 요청별 값이 아니다. */
  large_window_seen: boolean;
  mix: ContextMix | null;
}

export interface AnalysisTools {
  calls: number;
  /** 가동 중인 서브에이전트 수. 모르면 null. */
  subagents_running: number | null;
}

export interface ChangeArea {
  value: string | null;
  /** 도구 비식별화로 경로를 모르면 `redacted`. 계약 미정: 편집이 없을 때의 표기. */
  state: 'observed' | 'redacted';
}

export interface TaskTypeTag {
  /** 분류기 라벨(testing, debugging, ...). 열린 집합이다. */
  value: string;
  /** 요청 턴 중 이 유형의 비율. 계약 미정: 0~1 로 읽는다. */
  share: number;
  basis: 'classifier';
}

export interface RulesFileVersion {
  path: string;
  version: number;
  discovered_at: string;
}

export interface AnalysisTags {
  change_area: ChangeArea;
  task_type: TaskTypeTag | null;
  rules_files: RulesFileVersion[];
}

export interface PlanFlags {
  has_plan_file: boolean;
  used_plan_mode: boolean;
}

export interface AnalysisRepoStatus {
  branch: string | null;
  branch_state: 'observed' | 'undeterminable';
  branch_source: 'session_record_git_branch' | 'unknown';
  uncommitted_edited_files: number | null;
  uncommitted_state: 'observed' | 'undeterminable';
  uncommitted_source: 'session_git_snapshot' | 'unknown';
  uncommitted_scope: 'session_edited_files';
  uncommitted_reason: 'no_git_snapshot' | 'truncated_snapshot' | 'partial_snapshot' | 'unresolved_git_paths' | 'invalid_git_snapshot' | null;
  changed_at: string | null;
}

export interface SessionRepoStatusResponse {
  session_id: string;
  owner_view: boolean;
  analysis_state?: AnalysisState;
  repo_status?: AnalysisRepoStatus | null;
}

export interface AnalysisHeader {
  title: AnalysisTitle;
  name: AnalysisName | null;
  /** 세션 주인에게만 온다. */
  category?: SessionCategory;
  agent: string;
  model: string;
  branch: string;
  /** P4 counts only this session's edited paths; absent snapshots remain unknown. */
  repo_status?: AnalysisRepoStatus;
  project: AnalysisProject;
  accounts: AnalysisAccounts;
  started_at: string | null;
  last_activity_at: string | null;
  duration_seconds: number | null;
  active_seconds: number | null;
  counts: AnalysisCounts;
  tokens: AnalysisTokens;
  cost: AnalysisCost;
  context: AnalysisContext;
  tools: AnalysisTools;
  tags: AnalysisTags;
  plan_flags: PlanFlags;
}

/** 깊은 선택형. `building`·`truncated` 응답은 롤업 머리만 주므로 어느 필드든 빠질 수 있다 (§8.2 처리 순서 3). */
export type DeepPartial<T> = T extends readonly (infer U)[]
  ? readonly DeepPartial<U>[]
  : T extends object
    ? { [K in keyof T]?: DeepPartial<T[K]> }
    : T;

/* ============================================================
   신호 (signals) — 세션 주인에게만. not_applicable 키는 없다.
   ============================================================ */

/** `turn` 신호의 값은 턴 상태에서 `undeterminable`을 뺀 것이다. */
export type TurnSignalValue = Exclude<TurnStatus, 'undeterminable'>;

export interface UndeterminableSignal {
  state: 'undeterminable';
  value: null;
  reason: SignalUndeterminableReason;
}

export interface ObservedTurnSignal {
  state: 'observed';
  value: TurnSignalValue;
}
export type TurnSignal = ObservedTurnSignal | UndeterminableSignal;

/** 실시간 상태가 대기이면 `waiting_kind`가 붙는다. 판단 불가여도 붙을 수 있다. */
interface QuestionSignalExtra {
  waiting_kind?: WaitingKind | '' | null;
}
export interface ObservedQuestionSignal extends QuestionSignalExtra {
  state: 'observed';
  value: number;
}
export interface UndeterminableQuestionSignal extends QuestionSignalExtra {
  state: 'undeterminable';
  value: null;
  reason: SignalUndeterminableReason;
}
export type QuestionSignal = ObservedQuestionSignal | UndeterminableQuestionSignal;

export interface ObservedBackgroundSignal {
  state: 'observed';
  value: number;
  /** 계약 미정: 관측 상태에서의 모양. 판단 불가에서는 null이다. */
  parts?: Record<string, number> | null;
}
export interface UndeterminableBackgroundSignal extends UndeterminableSignal {
  parts: null;
}
export type BackgroundSignal = ObservedBackgroundSignal | UndeterminableBackgroundSignal;

export interface ObservedUncommittedSignal {
  state: 'observed';
  value: number;
  /**
   * 세션 중간부터 편집을 모은 값이면(`partial`) N>0은 "N개 이상"이다 (계획 §5.2).
   * 계약 미정: partial의 0은 "판단 불가"여야 하는데(§3.18) 이유 코드 여섯 개에 맞는 것이 없다. 서버가 `undeterminable`로
   * 보낼지 `observed 0 + at_least`로 보낼지 정해지지 않았다. 화면은 후자도 0개가 아니라 판단 불가로 그린다.
   */
  at_least: boolean;
  /** 이 개수가 처음 관측된(마지막으로 바뀐) 시각. */
  changed_at: string | null;
  /** 지금도 다시 확인하고 있는가. false면 이후 커밋이 반영되지 않았을 수 있다 (§3.18). */
  reevaluating: boolean;
}
export type UncommittedSignal = ObservedUncommittedSignal | UndeterminableSignal;

export interface ObservedTodoSignal {
  state: 'observed';
  value: number;
}
export type TodoSignal = ObservedTodoSignal | UndeterminableSignal;

export interface SessionSignals {
  turn?: TurnSignal;
  question?: QuestionSignal;
  background?: BackgroundSignal;
  uncommitted?: UncommittedSignal;
  todo?: TodoSignal;
}

/* ============================================================
   결과물 (outputs) · 요약 (recaps)
   ============================================================ */

export interface EditedFileItem {
  path: string;
  /** 활성 경로의 편집 횟수. */
  edits: number;
  subagent_edits: number;
  abandoned_edits: number;
}

export interface EditedFiles {
  /** `redacted`면 경로를 모른다(도구 정책). 프롬프트 정책이면 합계도 null이다. */
  state: 'observed' | 'redacted';
  /** 활성 경로에서 발생한 편집 횟수의 합. 관측 파일 수는 `items`의 고유 경로 수다. */
  active_total: number | null;
  abandoned_total: number | null;
  truncated: boolean;
  items: EditedFileItem[];
}

export interface LineChanges {
  added: number;
  removed: number;
  source: 'cost_state';
  /** 스냅샷의 일부만 합한 값이다. */
  partial: boolean;
}

export interface PullRequest {
  url: string;
  number: number;
  repository: string;
  first_seen_at: string;
  /** 처음 본 시각이 속한 턴. 속한 턴이 없으면 null. */
  turn_key: string | null;
}

export interface AnalysisOutputs {
  edited_files: EditedFiles;
  /** 스냅샷이 없으면 null. */
  lines: LineChanges | null;
  prs: { items: PullRequest[]; truncated: boolean };
}

export interface RecapItem {
  ts: string;
  text: string;
  /** 같은 문장이 몇 번 반복됐는지. 합쳐서 한 번만 온다. */
  repeats: number;
}

export interface AnalysisRecaps {
  state: RecapState;
  items: RecapItem[];
  /** items가 비었을 때 왜 없는지. */
  absent_reason?: RecapAbsentReason | null;
}

/* ============================================================
   턴 (D16)
   ============================================================ */

export interface TurnPreview {
  text: string | null;
  state: PreviewState;
}

export interface TurnCost {
  usd: number | null;
  source: TurnCostSource;
  reason: TurnCostReason | null;
}

export interface TurnContext {
  tokens: number | null;
  window: number | null;
  percent: number | null;
  source: ContextSource;
}

export interface TurnQuestionHeaders {
  tool_use_id: string;
  headers: string[];
  status: QuestionStatus;
}

export interface TurnEdited {
  count: number;
  /** 파일 이름 표본(최대 5개). */
  sample: string[];
  abandoned: number;
}

export interface AnalysisTurn {
  key: string;
  /** 시작 순번(버려진 분기 포함). */
  no: number;
  /** 활성 요청 번호. 요청이 아니면 null. */
  request_no: number | null;
  branch: TurnBranch;
  /** 대화 탭의 `from=` 앵커. */
  anchor_ts: string;
  started_at: string;
  ended_at: string;
  kind: InputKind;
  is_request: boolean;
  /** 작업 중에 들어와 턴을 열지 않은 입력 수. */
  extra_inputs: number;
  status: TurnStatus;
  status_reason: TurnStatusReason | null;
  preview: TurnPreview;
  images: number;
  /** Claude Code 의 permissionMode. 없으면 빈 문자열. 값 집합은 계약에 없다. */
  mode: string;
  /** 사용자가 도구 사용·플랜을 반려했다(`toolDenialKind = user-rejected`). */
  denied: boolean;
  login_email: string;
  api_requests: number;
  tokens: TokenBreakdown;
  cost: TurnCost;
  context: TurnContext;
  tools: { calls: number; subagents: number };
  question_headers: TurnQuestionHeaders[];
  edited: TurnEdited;
  prs: PullRequest[];
  last_assistant_message_id: string;
  /** 이 턴을 연 줄이 다른 세션에서 복사된 줄이다. */
  opener_copied: boolean;
  /**
   * 이 턴에 귀속된 커밋(D16). `commits.items` 중 `turn_key`가 이 턴인 것과 같은 집합이다 — 모든 턴의 `commits`를 합치면
   * 세션의 귀속된 커밋 전체가 되고, 커밋 하나는 한 턴에만 있다(분할). 해시를 모르는 커밋은 `short`가 null이다.
   * 통합 패스 전까지 선택(`?`)이다. 통합 뒤에는 필수이고, 비소유 관리자에게도 온다(커밋은 대화 내용이지 집계가 아니다).
   */
  commits?: TurnCommit[];
  /** 이 턴에서 갱신한 task·issue id (`tasks.items[].id`). 통합 패스 전까지 선택. */
  tasks?: string[];
  /** 이 턴에 속한 작업 항목 id (`record.work[].id`). 기록이 없으면 빈 배열. 통합 패스 전까지 선택. */
  work_ids?: string[];
}

/** 턴 안의 커밋 한 줄(D16). */
export interface TurnCommit {
  /** 짧은 해시. 해시를 모르는 커밋(레퍼런스의 "ハッシュ不明" 행)은 null. */
  short: string | null;
  subject: string;
  /** 이 커밋에 연결된 task·issue id. */
  tasks: string[];
}

/* ============================================================
   입력 · 질문 · 플랜 · 복사 · 서브에이전트 · 실시간
   ============================================================ */

export interface AnalysisInput {
  uuid: string;
  ts: string;
  /** 입력 번호(n/N의 n). */
  n: number;
  kind: InputKind;
  turn_key: string;
  images: number;
}

export interface QuestionOption {
  label: string;
  description: string;
  recommended: boolean;
}

export interface QuestionAnswer {
  labels: string[];
  free_text: string | null;
}

export interface QuestionItem {
  header: string;
  question: string;
  multi_select: boolean;
  options: QuestionOption[];
  answer: QuestionAnswer | null;
}

export interface AnalysisQuestion {
  tool_use_id: string;
  turn_key: string;
  ts: string;
  status: QuestionStatus;
  state: BodyState;
  items: QuestionItem[];
}

export interface AnalysisPlan {
  turn_key: string;
  ts: string;
  file_path: string | null;
  /** 파일 메타데이터가 수집된 경우에만. ExitPlanMode 시각과 구분한다. */
  file_updated_at?: string | null;
  decision: PlanDecision;
  /** 각 16KB에서 잘린다. `redacted`·`withheld`면 없다. */
  body?: string | null;
  state: BodyState;
}

export interface AnalysisCopies {
  state: CopiesState;
  /** 원본을 볼 수 없으면 null. */
  original_session_id: string | null;
  original_visible: boolean;
  /** `skipped_large_group`에서는 없다. */
  copied_lines?: number | null;
  copied_until?: string | null;
}

export interface AnalysisSubagents {
  unattributed_requests: number;
  unattributed_tokens: number;
}

/** 가동 상세(F4). null은 미관측값이며, 출처를 모르면 입출력 수치를 사용하지 않는다. */
export interface LiveProcessMetrics {
  pid: number | null;
  input_lines: number | null;
  output_lines: number | null;
  input_bytes: number | null;
  output_bytes: number | null;
  stdout_lines?: number | null;
  stdout_bytes?: number | null;
  stderr_lines?: number | null;
  stderr_bytes?: number | null;
  /** 실제 프로세스 스트림과 대화 기록량은 서로 다른 값이다. */
  io_source: 'process_stream' | 'transcript' | 'unknown';
  io_scope?: 'instance' | 'session' | 'unknown';
  io_mode?: 'pty' | 'pipes' | null;
  io_as_of?: string | null;
  io_epoch?: string | null;
  io_freshness?: 'fresh' | 'stale' | 'partial' | 'unknown';
  io_partial?: boolean;
  io_failure_code?: string | null;
}

export interface LiveInstance {
  status: LiveStatus;
  status_source?: string;
  waiting_kind: WaitingKind | '';
  heartbeat_at: string;
  name: string;
  /** 오래된 생존 행 (§8.4). */
  stale: boolean;
  /** 인스턴스별 관측값. 키가 없으면 PID와 입출력량 모두 알 수 없다. */
  process?: LiveProcessMetrics;
}

export interface AnalysisLive {
  instances: LiveInstance[];
  /** Transcript 측정은 세션 합계이며 프로세스 PID에 귀속되지 않는다. */
  transcript_io?: LiveProcessMetrics;
  collector: { heartbeat_at: string | null; last_stored_at: string | null };
}

/** 응답 배열이 상한에서 잘렸는지 (§8.2 응답 상한). 편집 파일·PR은 각자의 `truncated`를 가진다. */
export interface AnalysisTruncated {
  turns: boolean;
  abandoned: boolean;
  inputs: boolean;
  questions: boolean;
  plans: boolean;
  recaps: boolean;
  /** 본문(질문·플랜·recap) 합 512KB 상한. 넘으면 뒤의 본문이 비었다. */
  bodies: boolean;
}

/** 관리자에게만 온다. */
export interface AnalysisIdentities {
  user_ids: string[];
  profile_emails: string[];
  login_emails: string[];
  /** 여러 신원의 기록이 섞인 세션. */
  mixed: boolean;
}

/* ============================================================
   응답
   ============================================================ */

interface SessionAnalysisBase {
  session_id: string;
  /** Canonical turn membership revision, independent of execution trace snapshots. */
  turn_context_revision?: string;
  short_id: string;
  owner_view: boolean;
  /** 사실이 마지막으로 계산된 시각. 아직 사실이 없으면(building) 없다. */
  computed_at?: string;
  /** 새 기록이 재계산을 기다리기 시작한 시각. 기다리는 것이 없으면 null. */
  pending_since?: string | null;
  /** 세부 분석 계산을 worker가 처리 중이거나 대기 중인지. */
  analysis_pending?: boolean;
  rules_version?: number;
  identities?: AnalysisIdentities;
}

/** `ready`·`stale`: 사실이 있고 값을 보인다. `stale`은 규칙 판이 한 단계 옛것이라 "분석 규칙 갱신 중"을 함께 보인다. */
export interface SessionAnalysisFull extends SessionAnalysisBase {
  state: 'ready' | 'stale';
  rules_version: number;
  computed_at: string;
  header: AnalysisHeader;
  /** 세션 주인에게만. */
  signals?: SessionSignals;
  outputs: AnalysisOutputs;
  recaps: AnalysisRecaps;
  /** 활성 턴. */
  turns: AnalysisTurn[];
  /** 버려진 분기의 턴. 비용·토큰은 세션 합계에 들어 있다. */
  abandoned: AnalysisTurn[];
  inputs: AnalysisInput[];
  questions: AnalysisQuestion[];
  plans: AnalysisPlan[];
  /** 복사 술어가 켜졌고 복사본이 있을 때만. */
  copies?: AnalysisCopies;
  subagents: AnalysisSubagents;
  /** 세션 주인에게만. */
  live?: AnalysisLive;
  truncated: AnalysisTruncated;

  /*
   * ---- 패스 C 확장 ---------------------------------------------------------------------------------------------
   * 아래 필드는 지금 선택(`?`)이다. 패스 A 픽스처(부모 라우트)가 이 필드 없이 컴파일되도록 임시로 그렇게 뒀고, 응답에 키가
   * 없으면 화면은 "받지 못함"으로 그린다(없음이나 0이 아니다). 통합 패스가 `judgement`(주인) · `record` · `commits` · `tasks`를
   * 필수로 바꾸고 옛 픽스처를 채운다. `note`·`classification`은 주인에게만 오므로 통합 뒤에도 선택이다.
   */
  /** 작업 완료 판정(D8·D2·B3). 신호와 같은 정책이라 세션 주인에게만 온다. */
  judgement?: SessionJudgement;
  /** 세션 기록(D12~D15). LLM이 서버에서 대화 원문으로 쓴다. 기록이 없어도 객체는 온다(`state`가 이유). */
  record?: SessionRecord;
  /** 커밋(D10). */
  commits?: SessionCommits;
  /** task·이슈 갱신(D9). */
  tasks?: SessionTasks;
  /** 세션 메모(F6). 주인에게만. 메모가 없으면 null. */
  note?: SessionNote | null;
  /** 사용자 분류(F5). 주인에게만. */
  classification?: SessionClassification;
}

/**
 * 사실이 없거나(`building`, `none`), 일부만 있거나(`truncated`), 계산 대상이 아닌(`unsupported`) 응답.
 * 롤업 머리만 온다. 어느 필드든 빠질 수 있다.
 */
export interface SessionAnalysisPending extends SessionAnalysisBase {
  state: 'building' | 'none' | 'truncated' | 'unsupported';
  header?: DeepPartial<AnalysisHeader>;
}

export type SessionAnalysis = SessionAnalysisFull | SessionAnalysisPending;

/* ============================================================
   목록 행 (§8.3)
   ============================================================ */

export interface SessionRowContext {
  percent: number | null;
  tokens: number | null;
  source: ContextSource;
  large_window_seen: boolean;
}

/**
 * 목록 행의 신호. 값만 온다. null 은 판단 불가, 키가 없으면 해당 없음(Codex의 질문·백그라운드·할 일·미커밋)이다.
 * 분석 응답과 달리 이유, `at_least`, `waiting_kind`는 오지 않는다.
 */
export interface SessionRowSignals {
  turn?: TurnSignalValue | null;
  question?: number | null;
  background?: number | null;
  uncommitted?: number | null;
  todo?: number | null;
}

/** 목록 행의 `analysis` 객체. `category`와 `signals`는 세션 주인 범위에서만 온다. */
export interface SessionRowAnalysis {
  state: SessionRowAnalysisState;
  /** `state = withheld`의 이유. 계약 미정: 키 위치(§8.3은 괄호로만 적었다). */
  reason?: 'mixed_identity' | null;
  title?: AnalysisTitle;
  requests?: number | null;
  inputs?: number | null;
  context?: SessionRowContext;
  plan_flags?: PlanFlags;
  /** 계약 미정: 분석 응답의 `category`는 객체인데 §8.3은 키만 적었다. 키 문자열로 읽는다. */
  category?: SessionCategoryKey;
  signals?: SessionRowSignals;

  /*
   * ---- 패스 C 확장 (목록 카드 F1) ------------------------------------------------------------------------------
   * 모두 선택이다. 키가 없으면 카드는 그 칸을 그리지 않는다(0이나 없음이 아니다). 통합 패스가 정리한다.
   */
  /** 판정: 상태와 다섯 판정의 상태만(건수·이유는 상세에만 있다). 주인 범위에서만 온다. */
  judgement?: RowJudgement;
  /** 커밋 수. null은 모름(수집하지 않음·판단 불가), 0은 관측한 0. */
  commits?: number | null;
  /** 세션 메모의 첫 줄. 메모가 없으면 null. 주인 범위에서만 온다. */
  note?: string | null;
  /** 사용자 분류. 주인 범위에서만 온다. */
  classification?: SessionClassification;
  /** 세션 기록의 굵은 한 줄(`record.headline`). 기록이 없으면 null. */
  headline?: string | null;
  /** 주인에게만 오는 세션 기록 상태와 사유. */
  record_state?: RecordState;
  record_reason?: string | null;
  /**
   * 총 토큰(캐시 읽기·쓰기 포함)으로 상세의 "총 토큰"과 같은 값이다. 모르면 null.
   * 계약이 요구한 목록 행 필드에 없던 것을 더했다: 기존 행의 `input_tokens`·`output_tokens`에는 캐시가 빠져 카드의
   * "26.3M tok" 칩과 상세가 어긋난다.
   */
  tokens_total?: number | null;
}

/** 범주 칩의 개수. `GET /api/session-overview/categories`의 응답 모양은 계약 미정이라 칩이 받는 모양만 정했다. */
export type SessionCategoryCounts = Partial<Record<SessionCategoryKey, number>>;

/* ============================================================
   패스 C 확장 — 아래는 레퍼런스(research.md §3)의 D2·D8·D9·D10·D12~D15·E·F·A 요소가 필요로 하는 모양이다.
   서버는 이 모양을 지킨다. 응답 쪽 필드는 위(`SessionAnalysisFull`·`AnalysisTurn`·`SessionRowAnalysis`)에서 선택(`?`)으로
   걸었다. 통합 패스가 필수로 바꾼다.
   ============================================================ */

/* ---------------- 판정 (D8, D2, B3) ---------------- */

/**
 * 머리 상태 칩(D2). 서버가 아래 순서로 먼저 맞는 하나를 고른다.
 *   1. 살아 있는 실행 인스턴스(관측 범주 waiting·running·idle)가 있다
 *        - 사용자가 중단한 턴이 마지막이면 `interrupted`(중단 중), 아니면 `running`(실행 중)
 *   2. 살아 있지 않다
 *        - `turn_ended`가 undeterminable이면 `unknown`(턴이 끝났는지 모른다)
 *        - `turn_ended`가 fail이면 `ended_midway`(도중 종료)
 *        - 나머지 판정 중 하나라도 fail이면 `leftover`(작업 도중)
 *        - 판정 다섯 개가 모두 pass이면 `done`(완료), 실패 없이 판단 불가가 남으면 `unknown`
 * 신호를 합친 "닫아도 됨" 결론이 아니다(#682). 관측한 사실과 세션 기록을 칩 다섯 개로 보일 뿐이다.
 */
export type JudgementStatus = 'running' | 'done' | 'leftover' | 'ended_midway' | 'interrupted' | 'unknown';

/**
 * 툴바의 네 범주(B3). 살아 있는지와 무관하게 "지금 끝났다고 치면 어디인가"로 센다.
 *   `turn_ended`가 fail → `ended_midway` / 그 밖에 fail이 있다 → `leftover` / 다섯 개 모두 pass → `done` / 나머지는 `unknown`.
 * `bucket = null`은 구형 응답에서 판정을 받지 못한 경우이며 UI에서 unknown으로 셈한다.
 */
export type JudgementBucket = 'done' | 'leftover' | 'ended_midway' | 'unknown';

/** 판정 다섯. 응답은 이 순서로 정확히 다섯 개를 준다. */
export type JudgementCheckKey = 'turn_ended' | 'no_pending_reply' | 'no_background' | 'committed' | 'work_done';
export type JudgementCheckState = 'pass' | 'fail' | 'undeterminable';

/**
 * 판단 불가의 이유. 신호 이유와 세션 기록 생성/판정 이유를 함께 표현한다.
 *   `no_record`      세션 기록이 없어 작업 내용을 판단할 수 없다(레퍼런스의 "作業内容 未記入"). `work_done`만.
 *   `not_applicable` 그 에이전트는 이 신호를 낼 수 없다(Codex의 질문·백그라운드·미커밋·작업 내용). 해당 없음을 통과로 세지 않는다.
 */
export type JudgementReason = SignalUndeterminableReason | 'no_record' | 'not_applicable' | 'behind' | 'unvalidated_model';

/**
 * 판정 하나. 키별 규칙:
 *   turn_ended        신호 `turn`이 `ended`면 pass, 다른 관측값이면 fail(건수 없음), 판단 불가면 그 이유
 *   no_pending_reply  신호 `question` = 0이면 pass, 양수면 fail(count = 답하지 않은 질문 수)
 *   no_background     신호 `background` = 0이면 pass, 양수면 fail(count = 남은 백그라운드 작업 수)
 *   committed         신호 `uncommitted` = 0이면 pass, 양수면 fail(count = 편집 후 미커밋 파일 수). 세션 중간부터 모은 0은 판단 불가
 *   work_done         검증 전 모델 판정이면 undeterminable(unvalidated_model). 기록이 보지 못한 요청이 있으면 behind.
 *                     그 뒤 `work[]` 최상위 항목의 done=false가 하나라도 있으면 fail, done=null이나 항목 없음은 undeterminable(no_list).
 */
export interface JudgementCheck {
  key: JudgementCheckKey;
  state: JudgementCheckState;
  /** `fail`일 때만: 남은 것의 수. `turn_ended`에는 없다. */
  count?: number;
  /** `undeterminable`일 때만. */
  reason?: JudgementReason;
  /** 서버가 판단에 사용한 관측 출처. */
  source?: string;
}

type CheckOf<K extends JudgementCheckKey> = JudgementCheck & { key: K };

/** 다섯 판정을 정해진 순서로. 순서가 틀리면 컴파일이 막는다. */
export type JudgementChecks = readonly [
  CheckOf<'turn_ended'>,
  CheckOf<'no_pending_reply'>,
  CheckOf<'no_background'>,
  CheckOf<'committed'>,
  CheckOf<'work_done'>,
];

export interface SessionJudgement {
  status: JudgementStatus;
  /** 네 범주 중 하나. 판정 불가는 `unknown`; null은 구형 응답에서만 허용한다. */
  bucket: JudgementBucket | null;
  checks: JudgementChecks;
}

/** 목록 행의 판정(F1): 상태와 다섯 판정의 상태만. 건수와 이유는 상세에만 있다. */
export interface RowJudgement {
  status: JudgementStatus;
  /**
   * 계약이 요구한 "상태와 다섯 판정의 상태"에 더했다. 로드된 행만으로 개수 칩과 필터가 동작하려면 필요하다
   * (서버의 개수는 별도 엔드포인트가 준다).
   */
  bucket: JudgementBucket | null;
  checks: readonly [
    JudgementCheckState,
    JudgementCheckState,
    JudgementCheckState,
    JudgementCheckState,
    JudgementCheckState,
  ];
}

/** 툴바의 네 개수. 네 범주의 합이 판정 범위의 세션 수다. 주인 범위에서만 온다. */
export type SessionBucketCounts = Record<JudgementBucket, number>;

/* ---------------- 세션 기록 (D12~D15) ---------------- */

/**
 * LLM이 서버에서 대화 원문으로 쓴 세션 기록의 상태.
 *   ready       최신이다
 *   stale       기록은 있지만 그 뒤의 활동은 반영하지 않았다(`covers_until` 이후)
 *   generating  처음 만드는 중이다
 *   none        아직 만들지 않았다(생성 대상이 아니거나 아직 차례가 아님)
 *   withheld    AI 입력 동의·조직 설정이 없어 만들지 않았다. 기록이 없다는 뜻이 아니다
 *   redacted    비식별화로 대화 본문이 가려져 만들 수 없다
 *   failed      생성에 실패했다
 * `ready`·`stale`만 본문이 있다.
 */
export type RecordState = 'ready' | 'stale' | 'generating' | 'none' | 'withheld' | 'redacted' | 'failed' | 'disabled' | 'missing' | 'blocked' | 'waiting_budget';

/**
 * 종별 태그(D14, 복수). 화면 이름: implementation 구현, research 조사, maintenance 정비,
 * consultation 상담, bugfix 버그 수정, docs 문서.
 */
export type RecordKindTag = 'implementation' | 'research' | 'maintenance' | 'consultation' | 'bugfix' | 'docs';

export type ReviewKind = 'decision' | 'interpretation' | 'unverified';
export type RelatedKind = 'task' | 'issue' | 'pr' | 'feature';

/** 작업 항목의 종류(D15). 화면 이름: consultation 상담, request 의뢰, followup 추가 의뢰, finishing 마무리. */
export type WorkKind = 'consultation' | 'request' | 'followup' | 'finishing';
/** 하위 항목의 종류. 화면 이름: work 작업, check 확인. */
export type WorkChildKind = 'work' | 'check';

export interface RecordBullet {
  /** 백틱(`)으로 감싼 부분은 인라인 코드다. */
  text: string;
  /** 이 글머리표에 붙는 커밋(`commits.items[].short`). 칩으로 그린다. */
  commits: string[];
}

export interface ReviewItem {
  kind: ReviewKind;
  text: string;
  /** 근거가 된 요청 턴. 현재 턴 사실에서 빠졌으면 서버가 생략한다. */
  turn_key?: string;
}

export interface RelatedRef {
  kind: RelatedKind;
  id: string;
  /** Stable P4 task/item key; display ids can overlap across repositories. */
  item_key?: string;
  url?: string;
}

export interface RecordTags {
  kinds: RecordKindTag[];
  components: string[];
  screens: string[];
  related: RelatedRef[];
}

export interface WorkChild {
  kind: WorkChildKind;
  /** 모델의 완료 상태 제안. 검증 전에는 완료 판정으로 확정하지 않는다. null은 모델도 완료 여부를 말하지 못했다는 뜻이다. */
  done: boolean | null;
  text: string;
}

/** 작업 상황 체크리스트의 한 줄: 사용자가 한 요청 하나. */
export interface WorkItem {
  id: string;
  kind: WorkKind;
  /** 모델의 완료 상태 제안. 검증 전에는 완료 판정으로 확정하지 않는다. null은 모델도 완료 여부를 말하지 못했다는 뜻이다. */
  done: boolean | null;
  text: string;
  /** "→ 결정: …" 로 이어 붙는다(주로 상담). */
  decision?: string;
  /** 짧은 메모. */
  note?: string;
  /** 이 항목을 연 턴(`AnalysisTurn.key`). 줄을 누르면 타임라인이 그 턴으로 옮긴다. */
  turn_key?: string;
  /** 이 항목에서 만든 커밋의 짧은 해시. 모두 `commits.items[].short`에 있어야 한다. */
  commits: string[];
  /** 이 항목과 연결된 task·issue id(`tasks.items[].id`). */
  tasks: string[];
  /** 표시 id가 다른 저장소와 겹칠 때도 같은 P4 task를 보존하는 내부 키. */
  task_keys?: string[];
  children?: WorkChild[];
}

interface RecordMeta {
  /** 서버가 정한 생성 상태의 구체 사유 또는 실패 코드. */
  reason?: string | null;
  /** 기록을 만든(마지막으로 시도한) 시각. 만든 적 없으면 null. */
  generated_at: string | null;
  /** 이 시각까지의 대화를 반영했다. 만든 적 없으면 null. */
  covers_until: string | null;
}

/** 본문이 있는 기록(`ready`·`stale`). */
export interface RecordWithContent extends RecordMeta {
  state: 'ready' | 'stale';
  /** 굵은 한 줄. */
  headline: string;
  bullets: RecordBullet[];
  /** 검토 사항. */
  review: ReviewItem[];
  tags: RecordTags;
  /** 작업 상황 체크리스트. 사용자가 한 요청마다 한 항목이다. */
  work: WorkItem[];
}

/** 본문이 없는 기록. 이유가 `state`다. */
export interface RecordWithoutContent extends RecordMeta {
  state: 'generating' | 'none' | 'withheld' | 'redacted' | 'failed' | 'disabled' | 'missing' | 'blocked' | 'waiting_budget';
}

export type SessionRecord = RecordWithContent | RecordWithoutContent;

/** GET /api/session-search 의 주인 범위 일치 결과. 원문 대화는 검색하지 않는다. */
export type SessionRecordSearchField = 'title' | 'headline' | 'bullet' | 'review' | 'work' | 'commit' | 'task';

export interface SessionRecordSearchMatch {
  field: SessionRecordSearchField;
  text: string;
  turn_key?: string;
  /** task 저장 키. 표시용 display_id와 함께 해석할 때 쓴다. */
  item_key?: string;
}

export interface SessionRecordSearchResult {
  session_id: string;
  title: string | null;
  last_activity_at: string | null;
  matches: SessionRecordSearchMatch[];
}

export interface SessionRecordConsentInfo {
  runtime_key: string;
  disclosure_version: string;
  granted: boolean;
  granted_at: string | null;
  runtime: string;
  provider: string;
  provider_retention: string;
  disclosure: string;
  sends: string[];
  not_sends: string[];
  enabled: boolean;
  runtime_available: boolean;
  runtime_reason?: string;
}

export interface SessionRecordRangeStatus {
  state: 'fresh' | 'stale' | 'writing';
  written?: number;
  total: number;
  stale: number;
  writing: number;
  blocked: Record<string, number>;
  waiting_budget: boolean;
}

export interface SessionRecordVocabulary {
  repository_key: string;
  components: string[];
  screens: string[];
  components_closed: boolean;
  screens_closed: boolean;
}

/* ---------------- 커밋 (D10) ---------------- */

/**
 * observed         커밋을 관측했다(0건이어도 관측이다)
 * undeterminable   이 세션에서는 판단할 수 없다(도구 입력이 가려졌거나 git 기록을 받지 못했다)
 * not_collected    이 클라이언트·설정은 커밋을 모으지 않는다
 */
export type CommitsState = 'observed' | 'undeterminable' | 'not_collected';

export interface CommitItem {
  /** 전체 해시. 모르면 null(레퍼런스의 "ハッシュ不明" 행). */
  hash: string | null;
  /** 짧은 해시. 모르면 null. */
  short: string | null;
  subject: string;
  ts: string;
  /** 이 커밋이 귀속된 턴. 귀속하지 못했으면 없다. */
  turn_key?: string;
  repository?: string;
  branch?: string;
  /** 이 커밋에 연결된 task·issue id. */
  tasks: string[];
}

export interface SessionCommits {
  state: CommitsState;
  /** 관측한 전체 커밋 수. `observed`가 아니면 null(0이 아니다). 상한에서 잘렸어도 전체 수다. */
  total: number | null;
  items: CommitItem[];
  truncated: boolean;
}

/* ---------------- task·이슈 갱신 (D9) ---------------- */

export type TaskKind = 'task' | 'issue' | 'pr';

/** 네 동작 아이콘의 켜짐. 켜지 않은 동작은 흐리게 그린다. */
export interface TaskActions {
  added: boolean;
  completed: boolean;
  edited: boolean;
  commit_linked: boolean;
}

export interface TaskUpdate {
  id: string;
  kind: TaskKind;
  title: string;
  url?: string;
  actions: TaskActions;
  /** 한 줄 설명. */
  summary?: string;
}

/** 비어 있으면 "작업·이슈 갱신 없음"이다. */
export interface SessionTasks {
  items: TaskUpdate[];
}

/* ---------------- 메모와 분류 (F5, F6) ---------------- */

export interface SessionNote {
  text: string;
  updated_at: string;
}

/** 사용자 분류. 화면 이름: unsorted 미분류, consulting 상담 중, working 작업 중, later 나중에, done 완료. */
export type SessionClassification = 'unsorted' | 'consulting' | 'working' | 'later' | 'done';
export type SessionClassificationCounts = Record<SessionClassification, number>;

/* ---------------- 타임라인 (E1~E7) ---------------- */

export type TimelineRole = 'user' | 'assistant' | 'system';

/**
 * 타임라인 칸이 그리는 메시지 하나. 실제 앱은 기존 대화 API와 턴 사실(입력 번호, 출처, 질문·플랜 카드)로 만든다.
 *   - 사용자 입력에는 `n`·`of`·`source`가 있다(`AnalysisInput`과 같은 번호). 입력이 아닌 메시지에는 없다.
 *   - 질문 카드는 한 번만 그린다: 답한 질문은 답 입력(`source = 'answer'`) 메시지에 붙고, 답하지 않았거나 거절된 질문은
 *     assistant 메시지에 붙는다.
 */
export interface TimelineMessage {
  id: string;
  ts: string;
  role: TimelineRole;
  /** 이 메시지가 속한 턴(`AnalysisTurn.key`). 상세에서 턴이나 작업 항목을 누르면 이 값으로 옮긴다. */
  turn_key: string;
  n?: number;
  of?: number;
  source?: InputKind;
  /** Exact raw-record witness for an input source; unmatched user-role rows stay untrusted. */
  source_witness?: 'analysis_input_uuid' | 'unmatched_record';
  /** 본문. 줄바꿈을 그대로 둔다. 비식별화로 가려졌으면 없다. */
  text?: string;
  /** 붙인 이미지 수. */
  images?: number;
  /** 도구 호출 사이에 나온 assistant 중간 출력. 턴의 마지막 assistant 메시지는 아니다. */
  intermediate?: boolean;
  question?: AnalysisQuestion;
  plan?: AnalysisPlan;
}

/* ---------------- 전역 프레임 (A1~A6) ---------------- */

export type AccountProvider = 'claude' | 'codex';

export interface FrameAccount {
  /** 화면 이름("메인", "서브", "Codex"). */
  label: string;
  provider: AccountProvider;
  /** 5시간 창 사용률 0~100. 모르면 null(0이 아니다). */
  five_hour_pct: number | null;
  seven_day_pct: number | null;
  /** 창이 다시 시작되는 시각(UTC ISO). 모르면 null. */
  resets_at: { five_hour: string | null; seven_day: string | null };
  /** 다음 세션에 쓸 계정(A2의 라디오). Claude 계정 중 하나만 true다. Codex에는 라디오가 없다. */
  selected: boolean;
  /** Full provider/account key used only in the authenticated local profile selection call. */
  selectionKey?: string;
  /** True only after the local bridge reports an exact account match and the server says approved. */
  selectable?: boolean;
  five_hour_state?: 'measured' | 'stale' | 'unmeasured';
  seven_day_state?: 'measured' | 'stale' | 'unmeasured';
  five_hour_reason?: string;
  seven_day_reason?: string;
  attribution?: 'observed' | 'inferred' | 'unknown';
  source?: 'client_reported' | 'unknown';
}

/** 창 안의 한 점. x는 창 시작부터 지난 비율(0~1), y는 사용률(0~100). */
export interface SeriesPoint {
  x: number;
  y: number;
}

export interface SeriesBand {
  x: number;
  low: number;
  high: number;
}

/** 5시간 창 또는 7일 창 하나의 겹침 차트 데이터(A3, F9). */
export interface UsageWindowSeries {
  window: 'five_hour' | 'seven_day';
  /** 지금 창. */
  current: SeriesPoint[];
  /** 지난 창들. */
  past: SeriesPoint[][];
  /** 지난 창들의 중앙값. */
  median: SeriesPoint[];
  /** 지난 창들의 25~75% 범위. */
  band: SeriesBand[];
  /** Per-position count of distinct historical windows supporting the quantile. */
  sampleCounts?: { x: number; n: number }[];
  historyCount?: number;
  truncated?: boolean;
  source?: 'client_reported';
  reason?: string;
}

/** 하단 상태 표시줄(A6). */
export interface RepoStatus {
  branch: string | null;
  /** 미커밋 파일 수. 모르면 null(0이 아니다). */
  uncommitted: number | null;
}

export interface SessionFrame {
  accounts: FrameAccount[];
  /** 선택한 계정의 창별 차트. */
  series: { five_hour: UsageWindowSeries; seven_day: UsageWindowSeries };
  repo: RepoStatus;
}
