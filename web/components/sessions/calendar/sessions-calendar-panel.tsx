'use client';

import { SessionCalendarRequestError, type CalendarSessionWithOverview, type SessionCalendarResponse } from '@/lib/api';
import type { CalendarColorKey, SummaryGeneration } from '@/lib/session-calendar-types';
import type { UserInfo } from '@/lib/types';
import type { SessionClassification } from '@/lib/session-analysis-types';
import { CLASSIFICATION_LABEL } from '../analysis/list-model';
import { COLOR_KEYS } from './colors';
import { SessionCalendar } from './session-calendar';
import type { LocalDate } from './time-zone';

/**
 * 세션 화면의 캘린더 칸. 실제 `GET /api/session-calendar` 응답을 기존 제어형 `SessionCalendar`에 넘기고, 응답이 말하지 않는
 * 범위(관리자 사용자 선택, 계정 필터, 목록 전용 필터, 아직 없는 값)를 그 위에 적는다. 상태는 모두 부모가 갖는다.
 */

/**
 * 서버가 아직 값을 보내지 않을 수 있는 색 기준. 그 주의 모든 세션이 값을 보내지 않았을 때만 범례가 "(없음)"뿐인 이유를 적는다.
 * 태그는 서버가 null로 보낸 것(`unreported_tags`)만 미수집이고, 관측된 빈 목록은 미수집이 아니다. 완료 여부·프로필은 null이 "모름"이다.
 */
const UNREPORTED_TAGS: Partial<Record<CalendarColorKey, 'components' | 'screens' | 'kinds'>> = { component: 'components', screen: 'screens', kind: 'kinds' };
const unreported = (session: CalendarSessionWithOverview, key: CalendarColorKey): boolean => {
  const tag = UNREPORTED_TAGS[key];
  if (tag) return session.unreported_tags?.includes(tag) === true;
  if (key === 'status') return session.colors.status === null || session.colors.status === undefined;
  return key === 'profile' && session.colors.profile === null;
};
const ZONE_CHOICES = ['UTC', 'Asia/Seoul', 'America/New_York', 'Europe/London'];

type UserScope = 'user_id' | 'profile_email';

/**
 * 관리자 사용자 선택의 값. 서버의 접근 방식이 정한 신원 하나로만 고른다: 사용자 ID 방식이면 `user_id`, 이전 방식이면
 * `profile_email`이다. 그 방식으로 이름 붙일 수 없는 사용자는 null이다.
 */
const calendarUserKey = (user: UserInfo, scope: UserScope): string | null =>
  scope === 'user_id' ? (user.user_id ? `uid:${user.user_id}` : null) : (user.profile_email ? `email:${user.profile_email}` : null);

const errorText = (error: unknown): string =>
  error instanceof SessionCalendarRequestError ? error.message
    : error instanceof Error && error.message === 'Invalid session calendar response' ? '캘린더 응답 형식을 읽을 수 없습니다.'
      : '세션 캘린더를 읽지 못했습니다.';

interface SessionsCalendarPanelProps {
  timeZone: string;
  browserTimeZone: string;
  onTimeZoneChange: (timeZone: string) => void;
  weekStart: LocalDate;
  onWeekChange: (next: LocalDate) => void;
  nowMs: number;
  pxPerHour: number;
  onPxPerHourChange: (px: number) => void;
  colorKey: CalendarColorKey;
  onColorKeyChange: (key: CalendarColorKey) => void;
  data: SessionCalendarResponse | undefined;
  error: unknown;
  reloading: boolean;
  onReload: () => void;
  selectedId: string | null;
  onSelect: (sessionId: string, turnKey?: string, anchorTs?: string) => void;
  isAdmin: boolean;
  users: readonly UserInfo[] | undefined;
  usersError: boolean;
  selectedUser: string;
  onSelectedUserChange: (key: string) => void;
  /** 서버가 알려 준 접근 방식. 없으면 다른 사용자를 고를 수 없고 본인 캘린더만 읽는다. */
  userScope: UserScope | undefined;
  /** 상단 계정 필터(로그인 이메일). 사용자를 고르지 않는다. */
  loginEmail: string;
  /** 주간 AI 보고 실행처럼 서버 캘린더가 받지 않는 에이전트 범위. */
  agentUnsupported: boolean;
  /** 제목 검색·플랜·범주·정렬이 목록에 걸려 있다. 캘린더 서버는 이것들을 받지 않는다. */
  listOnlyFilters: boolean;
  classification?: SessionClassification;
  onClearClassification?: () => void;
  /** C18 owner-scoped generation status for this week. */
  summary?: SummaryGeneration;
  summaryLoading?: boolean;
  summaryRefreshing?: boolean;
  summaryError?: string | null;
  summaryScopeNote?: string;
  recordGenerationUnavailable?: string;
  onRegenerateSummaries?: () => void;
}

const SessionsCalendarPanel = ({
  timeZone, browserTimeZone, onTimeZoneChange, weekStart, onWeekChange, nowMs, pxPerHour, onPxPerHourChange,
  colorKey, onColorKeyChange, data, error, reloading, onReload, selectedId, onSelect,
  isAdmin, users, usersError, selectedUser, onSelectedUserChange, userScope, loginEmail, agentUnsupported, listOnlyFilters,
  summary, summaryLoading = false, summaryRefreshing = false, summaryError, summaryScopeNote, recordGenerationUnavailable, onRegenerateSummaries,
  classification, onClearClassification,
}: SessionsCalendarPanelProps) => {
  const zones = [...new Set([browserTimeZone, timeZone, ...ZONE_CHOICES].filter(Boolean))];
  const userOptions = userScope ? (users ?? []).map((user) => ({ key: calendarUserKey(user, userScope), user })) : [];
  const sessions = data?.sessions ?? [];
  const unreportedLabel = sessions.length > 0 && sessions.every((session) => unreported(session, colorKey))
    ? COLOR_KEYS.find((entry) => entry.key === colorKey)?.label : undefined;
  const selectedOutside = !!selectedId && !!data && !sessions.some((session) => session.session_id === selectedId);
  return (
    <div className="flex min-h-0 min-w-0 shrink grow-[3] basis-[480px] flex-col border-r border-border">
      <div className="shrink-0 space-y-1 border-b border-border-subtle px-3 py-2 text-[11px] text-ink-3">
        <div className="flex flex-wrap items-center gap-3">
          <label className="flex min-w-0 flex-wrap items-center gap-1">시간대
            <select aria-label="캘린더 시간대" value={timeZone} onChange={(event) => onTimeZoneChange(event.target.value)} className="min-h-11 max-w-full rounded border border-border bg-surface px-1 text-ink">
              {zones.map((zone) => <option key={zone} value={zone}>{zone === browserTimeZone ? `${zone} (브라우저)` : zone}</option>)}
            </select>
          </label>
          {isAdmin && (
            <label className="flex min-w-0 flex-wrap items-center gap-1">사용자
              <select aria-label="캘린더 사용자" value={selectedUser} disabled={!userScope} onChange={(event) => onSelectedUserChange(event.target.value)} className="min-h-11 max-w-full rounded border border-border bg-surface px-1 text-ink disabled:opacity-50">
                <option value="">본인 (기본)</option>
                {userOptions.map(({ key, user }) => key
                  ? <option key={key} value={key}>{user.profile_email || user.user_id}</option>
                  : <option key={`none:${user.profile_email}:${user.user_id}`} value="" disabled>{user.profile_email || '(이름 없음)'} (사용자 ID 없음)</option>)}
              </select>
            </label>
          )}
        </div>
        {isAdmin
          ? <p>관리자 캘린더는 기본으로 본인 세션을 보여 주며, 다른 사용자는 한 명만 고를 수 있습니다.{loginEmail && ` 상단 계정 필터(${loginEmail})는 사용자를 고르지 않고, 선택한 사용자의 세션 중 이 로그인 계정이 쓰인 세션으로 좁힙니다.`}</p>
          : loginEmail && <p>캘린더는 본인 세션 전체를 보여 주며 상단 계정 필터({loginEmail})는 적용하지 않습니다.</p>}
        {isAdmin && !userScope && <p role="status">서버가 접근 방식(사용자 ID 또는 프로필 이메일)을 알려 주지 않아 다른 사용자의 캘린더는 고를 수 없습니다. 본인 캘린더만 읽습니다.</p>}
        {isAdmin && userScope && usersError && <p role="alert" className="text-danger">사용자 목록을 읽지 못해 본인 캘린더만 고를 수 있습니다.</p>}
        {listOnlyFilters && <p>제목 검색·플랜·범주·판정·정렬은 목록에만 적용되며 캘린더에는 적용되지 않습니다.</p>}
        {classification && (
          <div className="flex flex-wrap items-center gap-2">
            <p role="status">{CLASSIFICATION_LABEL[classification]}{data && !error && !reloading && sessions.length === 0 ? ' 분류에 해당하는 세션이 이 주에는 없습니다.' : ' 분류만 표시 중입니다.'}</p>
            {onClearClassification && <button type="button" onClick={onClearClassification} className="min-h-11 rounded border border-border px-2 py-1 text-ink-2 hover:bg-surface-sunk">전체 분류 보기</button>}
          </div>
        )}
        {summaryScopeNote && <p>{summaryScopeNote}</p>}
        {recordGenerationUnavailable && <p role="status">{recordGenerationUnavailable}</p>}
        {summaryLoading && <p role="status">이 주의 기록 생성 상태를 읽는 중입니다.</p>}
        {summaryError && <p role="alert" className="text-danger">{summaryError}</p>}
        {unreportedLabel && (UNREPORTED_TAGS[colorKey]
          ? <p role="status">이 주 응답에는 &apos;{unreportedLabel}&apos; 값이 없습니다. 서버가 아직 이 값을 보내지 않아 범례가 (없음)으로만 표시됩니다.</p>
          : <p role="status">이 주 응답에서 &apos;{unreportedLabel}&apos; 값을 알 수 없어 범례가 (없음)으로 표시됩니다.</p>)}
        {selectedOutside && <p role="status">선택한 세션은 이 주 캘린더 범위에 없습니다. 오른쪽 뷰어는 그 세션을 그대로 보여 줍니다.</p>}
      </div>
      {agentUnsupported ? (
        <p role="status" className="px-4 py-6 text-center text-xs text-ink-3">주간 AI 보고 실행은 캘린더 대상이 아닙니다. 다른 에이전트 범위를 고르세요.</p>
      ) : error ? (
        <div role="alert" className="flex flex-col items-center gap-2 px-4 py-6 text-center text-xs text-danger">
          <p>{errorText(error)}</p>
          <button type="button" onClick={onReload} disabled={reloading} className="min-h-11 rounded border border-border px-2 py-0.5 text-ink-2 disabled:opacity-50">다시 읽기</button>
        </div>
      ) : (
        <SessionCalendar
          timeZone={timeZone}
          weekStart={weekStart}
          nowMs={nowMs}
          data={data}
          onWeekChange={onWeekChange}
          pxPerHour={pxPerHour}
          onPxPerHourChange={onPxPerHourChange}
          colorKey={colorKey}
          onColorKeyChange={onColorKeyChange}
          selectedId={selectedId}
          onSelect={onSelect}
          onReload={onReload}
          reloading={reloading}
          summary={summary}
          summaryRefreshing={summaryRefreshing}
          onRegenerateSummaries={onRegenerateSummaries}
        />
      )}
    </div>
  );
};

export { SessionsCalendarPanel, calendarUserKey };
