// @vitest-environment jsdom
import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import type { ComponentProps } from 'react';
import { fetchSessionCalendar, SessionCalendarRequestError } from '@/lib/api';
import { serverCalendar, WEEK_FROM, WEEK_TO } from './server-calendar.fixture';
// Go ReadSessionCalendar -> json.MarshalIndent output from the synthetic Testcontainers fixture at 25d07135.
import goPayload from './server-calendar.payload.json';
import { SessionsCalendarPanel } from './sessions-calendar-panel';

beforeAll(() => {
  Object.defineProperty(window, 'PointerEvent', { value: window.MouseEvent, configurable: true });
  HTMLCanvasElement.prototype.getContext = (() => null) as unknown as HTMLCanvasElement['getContext'];
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

const serve = (body: unknown, status = 200) => vi.stubGlobal('fetch', vi.fn(async () =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })));

type Props = ComponentProps<typeof SessionsCalendarPanel>;
const props = (over: Partial<Props>): Props => ({
  timeZone: 'Asia/Seoul', browserTimeZone: 'Asia/Seoul', onTimeZoneChange: vi.fn(),
  weekStart: { year: 2026, month: 10, day: 5 }, onWeekChange: vi.fn(), nowMs: Date.parse('2026-10-07T06:40:00Z'),
  pxPerHour: 30, onPxPerHourChange: vi.fn(), colorKey: 'project', onColorKeyChange: vi.fn(),
  data: undefined, error: null, reloading: false, onReload: vi.fn(), selectedId: null, onSelect: vi.fn(),
  isAdmin: false, users: undefined, usersError: false, selectedUser: '', onSelectedUserChange: vi.fn(), userScope: undefined,
  loginEmail: '', agentUnsupported: false, listOnlyFilters: false, ...over,
});

describe('real calendar response in the sessions calendar', () => {
  it('shows the active classification even when the calendar is empty and offers to clear it', async () => {
    const onClearClassification = vi.fn();
    render(<SessionsCalendarPanel {...props({ data: { ...serverCalendar(), sessions: [] }, classification: 'working', onClearClassification })} />);
    expect(screen.getByText('작업 중 분류에 해당하는 세션이 이 주에는 없습니다.')).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '전체 분류 보기' }));
    expect(onClearClassification).toHaveBeenCalledOnce();
  });
  it('draws the server-shaped week through the real fetcher and selects any returned session', async () => {
    serve(serverCalendar());
    const data = await fetchSessionCalendar({ from: WEEK_FROM, to: WEEK_TO });
    const onSelect = vi.fn();
    render(<SessionsCalendarPanel {...props({ data, onSelect })} />);
    expect(screen.getByTestId('week-session-count').textContent).toBe('4 세션');
    // ready-owned is complete; truncated and building are undeterminable, capped hit the 2,000 input cap.
    expect(screen.getByText(/확인이 필요한 세션 3개/).textContent).not.toContain('응답 형식 확인');
    expect(screen.queryByText(/읽을 수 없는 세션/)).toBeNull();
    expect(screen.queryByText(/응답 구간이 이 주와 다르거나/)).toBeNull();
    expect(document.querySelector('[data-session-id="ready-owned"]:not([data-faint])')).toBeTruthy();
    expect(document.querySelector('[data-session-id="truncated-row"][data-faint]')).toBeTruthy();
    expect(screen.queryByTestId('summary-state')).toBeNull();
    expect(screen.queryByRole('button', { name: '개요 다시 만들기' })).toBeNull();
    expect(screen.queryByText(/개요 생성 상태는 세션 기록 API가 아직 없어/)).toBeNull();
    await userEvent.click(document.querySelector('[data-session-id="building-row"]') as HTMLElement);
    expect(onSelect).toHaveBeenCalledWith('building-row');
  });

  it('passes actual C18 range status and refresh action to the calendar toolbar', async () => {
    const onRegenerateSummaries = vi.fn();
    render(<SessionsCalendarPanel {...props({ summary: { state: 'stale', stale: 2, blocked: { consent_required: 1 }, waiting_budget: true }, onRegenerateSummaries })} />);
    const summary = screen.getByTestId('summary-state');
    expect(summary.textContent).toContain('미생성·갱신 대기 2건');
    expect(summary.textContent).toContain('동의 필요 1건');
    await userEvent.setup().click(screen.getByRole('button', { name: '개요 다시 만들기' }));
    expect(onRegenerateSummaries).toHaveBeenCalledTimes(1);
  });

  it('says a tag legend is empty because the server sent null, but not for an observed empty list', async () => {
    serve(serverCalendar());
    const unreported = await fetchSessionCalendar({ from: WEEK_FROM, to: WEEK_TO });
    expect(unreported.sessions[0].unreported_tags).toEqual(['components', 'screens', 'kinds']);
    const { unmount } = render(<SessionsCalendarPanel {...props({ data: unreported, colorKey: 'screen' })} />);
    expect(screen.getByText(/'대상 화면' 값이 없습니다/)).toBeTruthy();
    expect(screen.getByRole('button', { name: '(없음) 4세션' })).toBeTruthy();
    unmount();
    const observed = serverCalendar();
    for (const session of observed.sessions) Object.assign(session.colors, { components: [], screens: [], kinds: [] });
    serve(observed);
    const empty = await fetchSessionCalendar({ from: WEEK_FROM, to: WEEK_TO });
    expect(empty.sessions.every((session) => !Object.hasOwn(session, 'unreported_tags'))).toBe(true);
    render(<SessionsCalendarPanel {...props({ data: empty, colorKey: 'screen' })} />);
    expect(screen.queryByText(/값이 없습니다/)).toBeNull();
    expect(screen.getByRole('button', { name: '(없음) 4세션' })).toBeTruthy();
  });

  it('describes a null status as unknown, not as a value the server has not sent', async () => {
    serve(serverCalendar());
    const data = await fetchSessionCalendar({ from: WEEK_FROM, to: WEEK_TO });
    render(<SessionsCalendarPanel {...props({ data, colorKey: 'status' })} />);
    expect(screen.getByText(/이 주 응답에서 '완료 여부' 값을 알 수 없어 범례가 \(없음\)으로 표시됩니다/)).toBeTruthy();
    expect(screen.queryByText(/서버가 아직 이 값을 보내지 않아/)).toBeNull();
  });

  it('describes a null profile as unknown, not as a value the server has not sent', async () => {
    serve(serverCalendar());
    const data = await fetchSessionCalendar({ from: WEEK_FROM, to: WEEK_TO });
    render(<SessionsCalendarPanel {...props({ data, colorKey: 'profile' })} />);
    expect(screen.getByText(/이 주 응답에서 '프로필' 값을 알 수 없어 범례가 \(없음\)으로 표시됩니다/)).toBeTruthy();
    expect(screen.queryByText(/서버가 아직 이 값을 보내지 않아/)).toBeNull();
  });

  it('keeps another user\'s per-session status hidden for an administrator', async () => {
    serve(serverCalendar({ ownerView: false, status: false }));
    const data = await fetchSessionCalendar({ from: WEEK_FROM, to: WEEK_TO, userId: 'other-uid' });
    expect(data.sessions.every((session) => !Object.hasOwn(session.colors, 'status'))).toBe(true);
    render(<SessionsCalendarPanel {...props({ data, isAdmin: true, colorKey: 'status', selectedUser: 'uid:other-uid', userScope: 'user_id',
      users: [{ user_id: 'other-uid', profile_email: 'other@example.test', login_emails: [], total_cost: 0 }], loginEmail: 'login@example.test' })} />);
    expect((screen.getByRole('button', { name: '완료 여부' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('고른 기준을 쓸 수 없어 프로젝트로 보입니다.')).toBeTruthy();
    expect((screen.getByRole('combobox', { name: '캘린더 사용자' }) as HTMLSelectElement).value).toBe('uid:other-uid');
    expect(screen.getByText(/상단 계정 필터\(login@example.test\)는 사용자를 고르지 않고/)).toBeTruthy();
  });

  it('offers other users only in the identity the server access mode reads', () => {
    const users = [{ user_id: 'uid-a', profile_email: 'a@example.test', login_emails: [], total_cost: 0 },
      { user_id: '', profile_email: 'legacy@example.test', login_emails: [], total_cost: 0 }];
    const { unmount } = render(<SessionsCalendarPanel {...props({ isAdmin: true, users, userScope: undefined })} />);
    const select = screen.getByRole('combobox', { name: '캘린더 사용자' }) as HTMLSelectElement;
    expect(select.disabled).toBe(true);
    expect(within(select).getAllByRole('option').map((option) => option.textContent)).toEqual(['본인 (기본)']);
    expect(screen.getByText(/서버가 접근 방식\(사용자 ID 또는 프로필 이메일\)을 알려 주지 않아/)).toBeTruthy();
    unmount();
    const uid = render(<SessionsCalendarPanel {...props({ isAdmin: true, users, userScope: 'user_id' })} />);
    expect((screen.getByRole('option', { name: 'a@example.test' }) as HTMLOptionElement).value).toBe('uid:uid-a');
    expect((screen.getByRole('option', { name: 'legacy@example.test (사용자 ID 없음)' }) as HTMLOptionElement).disabled).toBe(true);
    uid.unmount();
    render(<SessionsCalendarPanel {...props({ isAdmin: true, users, userScope: 'profile_email' })} />);
    expect((screen.getByRole('option', { name: 'a@example.test' }) as HTMLOptionElement).value).toBe('email:a@example.test');
    expect((screen.getByRole('option', { name: 'legacy@example.test' }) as HTMLOptionElement).value).toBe('email:legacy@example.test');
  });

  it('suppresses a response whose range is not the visible DST week', async () => {
    // New York, week of 2026-10-26: the fall-back Sunday makes it 169 hours.
    serve(serverCalendar({ from: '2026-10-26T04:00:00Z', to: '2026-11-02T05:00:00Z' }));
    const data = await fetchSessionCalendar({ from: '2026-10-26T04:00:00.000Z', to: '2026-11-02T05:00:00.000Z' });
    const { unmount } = render(<SessionsCalendarPanel {...props({ data, timeZone: 'America/New_York', weekStart: { year: 2026, month: 10, day: 26 } })} />);
    expect(screen.queryByText(/응답 구간이 이 주와 다르거나/)).toBeNull();
    unmount();
    render(<SessionsCalendarPanel {...props({ data, timeZone: 'America/New_York', weekStart: { year: 2026, month: 10, day: 19 } })} />);
    expect(screen.getByText(/응답 구간이 이 주와 다르거나/)).toBeTruthy();
  });

  it('reports disabled and failed reads with a retry instead of an endless loading state', async () => {
    serve({ error: 'session_analysis_disabled' }, 404);
    const error = await fetchSessionCalendar({ from: WEEK_FROM, to: WEEK_TO }).catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(SessionCalendarRequestError);
    const onReload = vi.fn();
    render(<SessionsCalendarPanel {...props({ error, onReload })} />);
    expect(screen.getByRole('alert').textContent).toContain('세션 분석이 서버에서 꺼져 있어');
    expect(screen.queryByText('캘린더를 읽는 중입니다')).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: '다시 읽기' }));
    expect(onReload).toHaveBeenCalled();
  });

  it('marks a selection that lies outside the visible week', async () => {
    serve(serverCalendar());
    const data = await fetchSessionCalendar({ from: WEEK_FROM, to: WEEK_TO });
    render(<SessionsCalendarPanel {...props({ data, selectedId: 'older-session' })} />);
    expect(screen.getByText(/선택한 세션은 이 주 캘린더 범위에 없습니다/)).toBeTruthy();
  });

  it('reads the Go-serialized calendar payload and draws, warns and selects exactly what it says', async () => {
    serve(goPayload.owner);
    const data = await fetchSessionCalendar({ from: goPayload.owner.from, to: goPayload.owner.to });
    expect(data.sessions.map((session) => session.session_id)).toEqual(['fixture-claude', 'fixture-codex-range', 'fixture-truncated-range', 'fixture-undeterminable']);
    expect(data.sessions[0].colors).toMatchObject({ project: { hash: 'real-project', name: null }, components: [], screens: [], kinds: [], status: null });
    expect(data.sessions[0].segments).toEqual(goPayload.owner.sessions[0].segments);
    expect(data.sessions[0].inputs).toEqual(goPayload.owner.sessions[0].inputs);
    // The payload reads a two-hour window; the screen asks for whole weeks, so only the
    // window is moved to the UTC week that contains it. Every session stays as serialized.
    const onSelect = vi.fn();
    render(<SessionsCalendarPanel {...props({ data: { ...data, from: '2026-10-05T00:00:00Z', to: '2026-10-12T00:00:00Z' },
      timeZone: 'UTC', browserTimeZone: 'UTC', weekStart: { year: 2026, month: 10, day: 5 }, nowMs: Date.parse('2026-10-05T12:00:00Z'), onSelect })} />);
    expect(screen.getByTestId('week-session-count').textContent).toBe('4 세션');
    expect(screen.queryByText(/읽을 수 없는 세션/)).toBeNull();
    // Codex range-only, truncated range-only and undeterminable inputs; the observed Claude session is complete.
    expect(screen.getByText(/확인이 필요한 세션 3개/).textContent).not.toContain('응답 형식 확인');
    expect(document.querySelector('[data-session-id="fixture-claude"]:not([data-faint])')).toBeTruthy();
    expect(document.querySelector('[data-session-id="fixture-undeterminable"]:not([data-faint])')).toBeTruthy();
    expect(document.querySelector('[data-session-id="fixture-codex-range"][data-faint]')).toBeTruthy();
    expect(document.querySelector('[data-session-id="fixture-truncated-range"][data-faint]')).toBeTruthy();
    await userEvent.click(document.querySelector('[data-session-id="fixture-codex-range"]') as HTMLElement);
    expect(onSelect).toHaveBeenCalledWith('fixture-codex-range');
    expect(data.sessions[1].overview?.session_id).toBe('fixture-codex-range');
  });

  it('keeps the Go-serialized administrator mixed row without a status value', async () => {
    serve(goPayload.admin_mixed);
    const data = await fetchSessionCalendar({ from: goPayload.admin_mixed.from, to: goPayload.admin_mixed.to, userId: 'picked' });
    expect(data.owner_view).toBe(false);
    expect(Object.hasOwn(data.sessions[0].colors, 'status')).toBe(false);
    render(<SessionsCalendarPanel {...props({ data: { ...data, from: '2026-10-05T00:00:00Z', to: '2026-10-12T00:00:00Z' },
      timeZone: 'UTC', browserTimeZone: 'UTC', weekStart: { year: 2026, month: 10, day: 5 }, colorKey: 'status', isAdmin: true })} />);
    expect((screen.getByRole('button', { name: '완료 여부' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByText(/읽을 수 없는 세션/)).toBeNull();
    expect(screen.queryByText(/활동 자료가 불완전/)).toBeNull();
  });
});
