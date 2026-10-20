/**
 * 주간 세션 캘린더 읽기 `GET /api/session-calendar?from=&to=` 의 응답 형태.
 *
 * 계획 §5.4(조회, 상한, 응답, 시간대), §3.1(색 기준 다섯, 상태 개수, 요약 생성 상태), p1b-design §2.3·§3.17·§8.6
 * (활동 조각과 입력 시각 점)이 근거다. 서버는 `internal/store/postgres_session_calendar.go`이고, `lib/api.ts`의
 * `fetchSessionCalendar`가 서버가 아직 모으지 않은 값(태그 null, 사실 없는 세션의 제목 출처 null, truncated 대체 사실의
 * 빈 출처)만 이 모양으로 맞춘다. 화면은 이 응답만으로 그린다 — 다른 요청을 더 보내지 않는다.
 *
 * 규칙
 * - 모든 시각은 UTC ISO 8601 이다. 날짜 열과 시각 눈금은 화면이 브라우저의 IANA 시간대로 계산한다.
 * - 구간은 반열린 `[from, to)` 이고 최대 14일이다.
 * - 숫자·문자열의 `null`은 "이 세션은 그 값을 모른다"는 뜻이다. 0이나 빈 문자열이 아니다.
 * - 선택(`?`) 필드는 "그 범위에서는 응답에 키가 없다"는 뜻이다(비소유 관리자의 `colors.status`).
 *   null인 배열·객체는 구버전/불완전 응답이며 화면은 모름으로 표시한다.
 *
 * 계약이 아직 정하지 않은 곳은 주석에 "제안"으로 적었다.
 */

import type {
  AnalysisProject,
  AnalysisState,
  AnalysisTitle,
  CostSource,
  PreviewState,
} from './session-analysis-types';

/** 한 요청의 최대 기간(일). 넘으면 서버가 400이다 (계획 §5.4 상한). */
export const CALENDAR_MAX_DAYS = 14;
/** 세션마다 돌려주는 입력 시각의 최대 개수. 넘으면 `inputs.truncated`가 참이다 (계획 §5.4 상한). */
export const CALENDAR_MAX_INPUTS = 2_000;

/**
 * 요청의 쿼리. 서버(`handleSessionCalendar`)가 읽는 이름 그대로다. 목록 툴바의 제목 검색·플랜·범주·정렬은 받지 않는다.
 */
export interface SessionCalendarQuery {
  /** 포함. UTC ISO. */
  from: string;
  /** 제외. UTC ISO. `to - from <= 14일`. */
  to: string;
  /** 관리자가 사용자 한 명을 고를 때. 사용자 ID 접근 방식의 배포에서 쓴다. 기본은 본인 세션이다. */
  user_id?: string;
  /** 관리자가 사용자 한 명을 고를 때. 프로필 이메일 접근 방식의 배포에서 쓴다. `user_id`와 함께 보내지 않는다. */
  profile_email?: string;
  /** 관리자의 계정 필터. 선택한 사용자의 세션 중 이 로그인 계정이 쓰인 세션으로 좁힌다. 일반 사용자는 서버가 무시한다. */
  login_email?: string;
  /** 쉼표로 이은 프로젝트 해시. */
  project_hashes?: string;
  agent?: '' | 'claude' | 'codex' | 'other';
  source?: '' | 'interactive' | 'headless' | 'all';
  /** `1`이면 분기·이어짐을 뿌리 세션으로 접는다. */
  assembled?: '' | '0' | '1';
  classification?: string;
}

/**
 * 색 기준(계획 §3.1 C4). 레퍼런스의 다섯 — 완료 여부, 컴포넌트, 화면, 종별, 프로필 — 이 앞에 오고, 프로젝트·에이전트·모델이
 * 뒤따른다. 화면의 "색 기준" 선택지 순서와 같다.
 */
export type CalendarColorKey = 'status' | 'component' | 'screen' | 'kind' | 'profile' | 'project' | 'agent' | 'model';

/**
 * 완료 여부. 작업 완료 판정(D8: 턴 종료, 답 대기 없음, 백그라운드 작업 없음, 커밋됨, 작업 내용 완료)과 생존 행을 네 칸으로 접은 값이다.
 * - `done` 완료: 판정 칩이 모두 통과다.
 * - `leftover` 하다 만 것: 남은 것이 있다(답 없는 질문, 백그라운드 작업, 커밋하지 않은 편집, 미완료 작업 항목).
 * - `midway` 도중 종료: 턴이 중단·오류·응답 없음으로 끝났거나 실행 중이던 것이 사라졌다.
 * - `running` 실행 중: 살아 있는 인스턴스가 일하거나 응답을 기다린다.
 * - `unknown` 판정 불가: 판정 자료가 없어 완료나 미완료로 분류할 수 없다.
 * 목록 툴바는 실행 중을 "하다 만 것"에 세고 판정 불가는 독립된 칩으로 센다.
 */
export type CalendarStatus = 'done' | 'leftover' | 'midway' | 'running' | 'unknown';

/**
 * 한 세션의 색 기준 값들. 화면은 이 중 하나를 골라 그 주의 값별로 세고 색을 준다(C5).
 *
 * 세션 기록의 태그(`components`, `screens`, `kinds`)는 여러 값일 수 있다. **첫 값이 그 세션의 색**이다 — 범례의 세션 수
 * 합이 그 주의 세션 수와 같아야 하기 때문이다. 나머지 값은 호버에 보인다.
 */
export interface CalendarColors {
  /** 같은 이름의 프로젝트가 둘일 수 있어 해시로 가른다. 없으면 null. */
  project: AnalysisProject | null;
  agent: string;
  model: string | null;
  /** 계정 프로필의 이름("메인", "서브", "Codex" 같은). 모르면 null. */
  profile: string | null;
  /** 컴포넌트·화면·종별: 세션 기록(P5)이 붙인 태그. 없으면 빈 배열이고 범례에서 "(없음)"이다. */
  components: string[];
  screens: string[];
  /** 종별은 구현, 조사, 정비, 상담, 버그 수정, 문서 중 하나 이상이다. */
  kinds: string[];
  /**
   * 완료 여부. 세션 주인 범위에서만 온다 — 남의 세션의 판정은 신호에서 나오므로 관리자가 남의 세션을 볼 때는 키가 없다
   * (계획 §1 비목표, §5.4 관리자). 주인 범위에서 모르면(사실이 아직 없음) null.
   */
  status?: CalendarStatus | null;
}

/** 호버에 보일 비용. 출처를 함께 준다(목록·분석 탭과 같은 `CostSource`). */
export interface CalendarCost {
  /** 모르면 null. 0이 아니다. */
  usd: number | null;
  source: CostSource;
}

/** 세션의 전체 활동 범위(첫 활동~마지막 활동). 구간 밖까지 이어질 수 있다. */
export interface CalendarRange {
  start: string;
  end: string;
}

/**
 * 활동 조각 하나(`session_activity_segments`의 한 행). 메인 스레드 기록을 15분 이상 비는 곳에서 끊은 것이다.
 * 구간과 겹치는 조각을 **자르지 않고** 그대로 준다(구간 밖으로 이어지는 조각은 화면이 열 경계에서 자른다).
 * 복사본 줄과 서브에이전트 기록은 들어 있지 않다(계획 C8).
 */
export interface CalendarSegment {
  start: string;
  /** `end >= start`. 기록 한 줄뿐인 조각은 같다. */
  end: string;
  records: number;
  /** 요청 입력과 작업 중 사람 입력의 수. 비식별화로 사람 입력과 도구 결과를 가를 수 없으면 null — 0이 아니다. */
  human_inputs: number | null;
}

/** 사람 입력 시각 하나. 띠 안의 가로선과 10분 밀도 띠가 이것으로 그려진다(C9, C11). */
export interface CalendarInput {
  ts: string;
  /** 이 입력이 속한 턴. 대화 탭으로 옮길 때 쓴다. */
  turn_key: string;
  /**
   * 요청의 첫 줄. `state`가 `ok`가 아니면 `text`는 null이고 화면은 시각만 보인다(C16).
   * `redacted` 비식별화, `withheld` 서버에 올리지 않음, `none` 첫 줄이 없다.
   */
  preview: { text: string | null; state: PreviewState };
}

/** 입력 시각 목록. */
export interface CalendarInputs {
  /**
   * `observed` 입력 시각을 안다. `undeterminable` 모른다 — 비식별화로 사람 입력과 도구 결과를 가를 수 없거나,
   * 사실이 없는 세션(`facts`가 `ready`·`stale`이 아님)이다. 빈 `items`는 "입력 0"이 아니라 "모름"일 수 있다.
   */
  state: 'observed' | 'undeterminable';
  /** 그 구간 것만, 시각 순으로 최대 `CALENDAR_MAX_INPUTS`개. 초과 응답은 화면이 시각순으로 이른 입력 2,000건만 그린다. */
  items: CalendarInput[];
  /** 구간 안의 입력이 상한을 넘어 앞의 상한만큼만 담았다. */
  truncated: boolean;
}

export interface CalendarSession {
  session_id: string;
  /** 분석 응답의 제목과 같다(`text`/`source`/`state`). 가려졌거나 없을 수 있다. */
  title: AnalysisTitle;
  colors: CalendarColors;
  /**
   * 호버에 보일 요청 수. 턴 경계를 판단할 수 없으면 null. 0은 "요청 없는 세션"(자동 실행 등)이라 목록의
   * "요청 없는 세션도 보기"가 꺼져 있으면 목록과 캘린더에서 함께 빠진다(B5).
   */
  requests: number | null;
  cost: CalendarCost;
  /**
   * 이 세션의 분석 사실 상태(분석 API의 `state`와 같은 값). `ready`·`stale`이면 `segments`와 `inputs`가 있다.
   * 그 밖(`truncated`, 제안: `building`·`unsupported`·`none`)은 조각이 없어 화면이 `range`만 흐리게 그린다.
   * 혼합 신원 세션(`withheld`)은 세션 주인에게 오지 않는다(분석 API가 404인 것과 같다).
   */
  facts: AnalysisState;
  range: CalendarRange;
  segments: CalendarSegment[];
  inputs: CalendarInputs;
}

export interface SessionCalendar {
  /** 읽은 구간. 요청한 값을 그대로 돌려준다. 화면은 현재 주를 모두 포함하고 최대 14일인지 검사한다. */
  from: string;
  to: string;
  /**
   * 세션 주인 범위인가. 거짓이면(관리자가 남의 세션을 봄) 세션마다 `colors.status`가 없다 — 화면은 "완료 여부" 색 기준을
   * 고를 수 없게 한다(계획 §1 비목표, §5.4 관리자).
   */
  owner_view: boolean;
  sessions: CalendarSession[];
}

/**
 * 요약 생성 상태(C18). 응답이 아니라 화면이 별도로 받는 상태다 — 세션 기록을 쓰는 작업(P5)이 알려 준다.
 * - `fresh` 개요가 최신이다("개요는 최신", 누를 수 없다).
 * - `stale` 쓰지 못한 세션이 있다("개요 다시 만들기").
 * - `writing` 지금 쓰는 중이다.
 * `written`은 방금 끝난 작업이 쓴 건수다. 있으면 "N건을 썼습니다"로 보인다.
 */
export interface SummaryGeneration {
  state: 'fresh' | 'stale' | 'writing';
  written?: number | null;
  total?: number;
  stale?: number;
  writing?: number;
  /** 만들 수 없거나 정책상 막힌 세션 수를 reason별로 받는다. */
  blocked?: Record<string, number>;
  waiting_budget?: boolean;
}

/** 조각이 있어 띠로 그리는 사실 상태. */
export const hasSegments = (facts: AnalysisState): boolean => facts === 'ready' || facts === 'stale';
