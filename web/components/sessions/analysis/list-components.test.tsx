// @vitest-environment jsdom
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { A_ID, C_ID, PREVIEW_SESSIONS, rowFor } from '@/app/test/session-analysis/fixtures';
import { DETAIL_NOW } from '@/app/test/session-analysis/detail/builders';
import { flagship } from '@/app/test/session-analysis/detail/flagship';
import type { JudgementBucket, SessionClassification } from '@/lib/session-analysis-types';
import type { SessionOverview } from '@/lib/types';
import { BucketCountChips } from './bucket-chips';
import { ClassificationSelect } from './classification-select';
import { ClassificationTabs, type ClassificationFilter } from './classification-tabs';
import { EndedGroupHeader, RunningGroupHeader } from './list-groups';
import { SessionNoteEditor, type NoteSaveState, type NoteTab } from './note-editor';
import { SessionAnalysisRow } from './session-analysis-row';

afterEach(cleanup);

const textOf = (html: string): string => html.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ');
const noop = () => undefined;

const renderRow = (session: SessionOverview, extra: Partial<Parameters<typeof SessionAnalysisRow>[0]> = {}): string =>
  renderToStaticMarkup(createElement(SessionAnalysisRow, { session, selected: false, onSelect: noop, now: DETAIL_NOW, ...extra }));

describe('목록 카드(F1)', () => {
  const row = flagship().row;

  it('프로젝트 이름을 툴팁이 아니라 화면 글자로 보인다', () => {
    const t = textOf(renderRow({ ...row, project_name: 'cctrace_internal' }));
    expect(t).toContain('cctrace_internal');
  });

  it('제목, 요약 한 줄, 관측 신호, 상대 시각, 계정, 요청 수, 토큰·비용, 컨텍스트, 커밋 수, 메모를 보인다', () => {
    const html = renderRow(row);
    const t = textOf(html);
    expect(t).toContain('턴 끝남');
    expect(t).toContain('알림 센터에 필터와 읽음 처리 넣기');
    expect(t).toContain('알림 센터에 종류별 필터와 읽음 처리를 넣었다');
    expect(t).toContain('6분 전');
    expect(t).toContain('dahye.sub');
    expect(t).toContain('요청 14');
    expect(t).toContain('26.3M tok');
    expect(t).toContain('$7.57');
    expect(t).toContain('30%');
    expect(html).toContain('aria-label="컨텍스트 30%');
    expect(t).toMatch(/커밋\s*10/);
    expect(t).toContain('다음 세션에서 키보드 단축키 문서를 확인할 것.');
  });

  it('판정 결론과 다섯 기호를 그리지 않는다', () => {
    const html = renderRow(row);
    expect(html).not.toContain('data-check=');
    expect(html).not.toContain('작업 완료 판정');
  });

  it('선택된 카드는 aria-pressed', () => {
    expect(renderRow(row, { selected: true })).toContain('aria-pressed="true"');
    expect(renderRow(row)).toContain('aria-pressed="false"');
  });

  it('누르면 그 세션으로 onSelect', async () => {
    const onSelect = vi.fn();
    render(createElement(SessionAnalysisRow, { session: row, selected: false, onSelect, now: DETAIL_NOW }));
    await userEvent.click(screen.getByRole('button'));
    expect(onSelect).toHaveBeenCalledWith(row);
  });

  it('커밋 수가 null이면 0이 아니라 "?"이고, 필드가 없으면 칸이 없다', () => {
    const base = row.analysis;
    if (!base) throw new Error('flagship row has analysis');
    const unknown = textOf(renderRow({ ...row, analysis: { ...base, commits: null } }));
    expect(unknown).toMatch(/커밋\s*\?/);
    const { commits, ...noCommits } = base;
    void commits;
    const withoutCommits = renderRow({ ...row, analysis: noCommits });
    expect(withoutCommits).not.toContain('커밋 수를 알 수 없습니다');
    expect(withoutCommits).not.toContain('>커밋<');
    expect(textOf(renderRow({ ...row, analysis: { ...base, commits: 0 } }))).toMatch(/커밋\s*0/);
  });

  it('토큰을 모르면 "토큰 알 수 없음"이다(0이 아니다)', () => {
    const base = row.analysis;
    if (!base) throw new Error('flagship row has analysis');
    const html = renderRow({ ...row, analysis: { ...base, tokens_total: null } });
    expect(html).toContain('aria-label="토큰 알 수 없음"');
    expect(textOf(html)).not.toContain('0 tok');
  });

  it('메모가 없으면(null) 메모 줄이 없다', () => {
    const base = row.analysis;
    if (!base) throw new Error('flagship row has analysis');
    expect(textOf(renderRow({ ...row, analysis: { ...base, note: null } }))).not.toContain('키보드 단축키');
  });

  it('판정이 오지 않은 행은 범주 칩과 신호 축약이 대신한다 (패스 A의 A)', () => {
    const a = PREVIEW_SESSIONS.find((f) => f.id === A_ID);
    if (!a) throw new Error('fixture A missing');
    const t = textOf(renderRow(rowFor(a, 'owner')));
    expect(t).toContain('응답 대기');
    expect(t).toContain('질문 1');
    expect(t).toContain('미커밋 3');
    expect(t).toContain('요청 12');
    expect(t).toContain('In ');
  });

  it('비소유 범위는 판정·신호·범주를 그리지 않고 요청 수는 그린다 (패스 A의 A)', () => {
    const a = PREVIEW_SESSIONS.find((f) => f.id === A_ID);
    if (!a) throw new Error('fixture A missing');
    const html = renderRow(rowFor(a, 'admin'));
    expect(html).not.toContain('data-check=');
    expect(textOf(html)).toContain('요청 12');
  });

  it('요청 수가 null이면 점선 "?"이다 (패스 A의 C)', () => {
    const c = PREVIEW_SESSIONS.find((f) => f.id === C_ID);
    if (!c) throw new Error('fixture C missing');
    expect(renderRow(rowFor(c, 'owner'))).toMatch(/요청\s*<span[^>]*title="[^"]*0이 아닙니다\."/);
  });

  it('"떼기" 버튼은 묶음 안의 카드에만 있고 눌러도 카드가 선택되지 않는다', async () => {
    expect(textOf(renderRow(row))).not.toContain('떼기');
    const onDismiss = vi.fn();
    const onSelect = vi.fn();
    render(createElement(SessionAnalysisRow, { session: row, selected: false, onSelect, onDismiss, now: DETAIL_NOW }));
    await userEvent.click(screen.getByRole('button', { name: /떼기/ }));
    expect(onDismiss).toHaveBeenCalledWith(row);
    expect(onSelect).not.toHaveBeenCalled();
  });
});

describe('묶음 머리(F2, F3)', () => {
  it('실행 중인 세션 N건과 처리 중 · 완료 · 중단 중 개수. 작업 도중·판정 불가는 있을 때만', () => {
    const t = textOf(renderToStaticMarkup(createElement(RunningGroupHeader, { counts: { total: 3, processing: 1, waiting: 0, done: 1, interrupted: 1, leftover: 0, unknown: 0 } })));
    expect(t).toContain('실행 중인 세션 3건');
    expect(t).toMatch(/처리 중\s*1\s*완료\s*1\s*중단 중\s*1/);
    expect(t).not.toContain('작업 도중');
    expect(t).not.toContain('판정 불가');
    const more = textOf(renderToStaticMarkup(createElement(RunningGroupHeader, { counts: { total: 7, processing: 1, waiting: 1, done: 1, interrupted: 1, leftover: 2, unknown: 1 } })));
    expect(more).toMatch(/응답 대기\s*1/);
    expect(t).not.toContain('응답 대기');
    expect(more).toMatch(/작업 도중\s*2/);
    expect(more).toMatch(/판정 불가\s*1/);
  });

  it('함께 끝난 묶음의 "한꺼번에 떼기"는 묶음을 넘겨 부른다', async () => {
    const group = { key: 'ended-1', label: '10/10 13:05에 함께 끝난 3건', endedAt: '2026-10-10T04:05:00Z', rows: [] };
    const onDismissAll = vi.fn();
    render(createElement(EndedGroupHeader, { group, onDismissAll }));
    expect(screen.getByText('10/10 13:05에 함께 끝난 3건')).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '한꺼번에 떼기' }));
    expect(onDismissAll).toHaveBeenCalledWith(group);
  });
});

describe('분류 탭(F5)', () => {
  const counts = { unsorted: 12, consulting: 3, working: 5, later: 2, done: 30 };

  const Harness = ({ initial = 'all' as ClassificationFilter, onChange = vi.fn() }) => {
    const [value, setValue] = useState<ClassificationFilter>(initial);
    return createElement(ClassificationTabs, {
      value,
      counts,
      total: 60,
      onChange: (next: ClassificationFilter) => {
        onChange(next);
        setValue(next);
      },
      panelId: 'list',
    });
  };

  it('전체와 다섯 분류가 개수와 함께 탭으로 있고 선택된 탭만 Tab으로 닿는다', () => {
    render(createElement(Harness));
    const tabs = screen.getAllByRole('tab');
    expect(tabs.map((t) => t.textContent)).toEqual(['전체60', '미분류12', '상담 중3', '작업 중5', '나중에2', '완료30']);
    expect(tabs[0].getAttribute('aria-selected')).toBe('true');
    expect(tabs[0].getAttribute('tabindex')).toBe('0');
    expect(tabs[1].getAttribute('tabindex')).toBe('-1');
  });

  it('클릭과 화살표, Home, End로 옮긴다', async () => {
    const onChange = vi.fn();
    render(createElement(Harness, { onChange }));
    await userEvent.click(screen.getByRole('tab', { name: /작업 중/ }));
    expect(onChange).toHaveBeenLastCalledWith('working');
    await userEvent.keyboard('{ArrowRight}');
    expect(onChange).toHaveBeenLastCalledWith('later');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: /나중에/ }));
    await userEvent.keyboard('{End}');
    expect(onChange).toHaveBeenLastCalledWith('done');
    await userEvent.keyboard('{ArrowRight}');
    expect(onChange).toHaveBeenLastCalledWith('all');
    await userEvent.keyboard('{ArrowLeft}');
    expect(onChange).toHaveBeenLastCalledWith('done');
    await userEvent.keyboard('{Home}');
    expect(onChange).toHaveBeenLastCalledWith('all');
  });

  it('개수 응답이 없을 때 0건으로 추정하지 않고 라벨만 표시한다', () => {
    render(createElement(ClassificationTabs, { value: 'all', onChange: noop }));
    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['전체', '미분류', '상담 중', '작업 중', '나중에', '완료']);
  });

  it('비소유 범위는 탭 대신 안내다', () => {
    const t = textOf(renderToStaticMarkup(createElement(ClassificationTabs, { value: 'all', counts, total: 5, onChange: noop, unavailable: true })));
    expect(t).toContain('분류와 메모는 세션 주인에게만 제공됩니다');
  });

  it('분류 선택 상자는 값을 알리고 스스로 바꾸지 않는다', async () => {
    const onChange = vi.fn<(c: SessionClassification) => void>();
    render(createElement(ClassificationSelect, { value: 'unsorted', onChange, sessionLabel: 'c8e41d27' }));
    const select = screen.getByRole('combobox', { name: 'c8e41d27의 분류' });
    await userEvent.selectOptions(select, '나중에');
    expect(onChange).toHaveBeenCalledWith('later');
    expect((select as HTMLSelectElement).value).toBe('unsorted');
  });
});

describe('상태별 개수 칩(B3)', () => {
  const counts = { done: 250, leftover: 356, ended_midway: 23, unknown: 0 };

  it('완료 · 하다 만 것 · 도중 종료 · 판정 불가 네 범주와 개수를 항상 보인다', () => {
    const t = textOf(renderToStaticMarkup(createElement(BucketCountChips, { counts, value: null, onChange: noop })));
    expect(t).toMatch(/완료\s*250\s*하다 만 것\s*356\s*도중 종료\s*23\s*판정 불가\s*0/);
    expect(textOf(renderToStaticMarkup(createElement(BucketCountChips, { counts: { ...counts, unknown: 4 }, value: null, onChange: noop })))).toMatch(/판정 불가\s*4/);
  });

  it('누르면 그 상태, 다시 누르면 해제(null)', async () => {
    const onChange = vi.fn<(b: JudgementBucket | null) => void>();
    const { rerender } = render(createElement(BucketCountChips, { counts, value: null, onChange }));
    await userEvent.click(screen.getByRole('button', { name: /완료\s*250/ }));
    expect(onChange).toHaveBeenLastCalledWith('done');
    rerender(createElement(BucketCountChips, { counts, value: 'done', onChange }));
    expect(screen.getByRole('button', { name: /완료\s*250/ }).getAttribute('aria-pressed')).toBe('true');
    await userEvent.click(screen.getByRole('button', { name: /완료\s*250/ }));
    expect(onChange).toHaveBeenLastCalledWith(null);
  });

  it('비소유 범위는 개수 대신 안내다', () => {
    expect(textOf(renderToStaticMarkup(createElement(BucketCountChips, { counts, value: null, onChange: noop, unavailable: true })))).toContain(
      '상태별 개수는 세션 주인에게만 제공됩니다',
    );
  });
});

describe('세션 메모 편집기(F6)', () => {
  const Harness = ({ initialTab = 'session' as NoteTab, initialOpen = true, saveState = 'saved' as NoteSaveState, onPick = vi.fn(), onSessionChange = vi.fn() }) => {
    const [tab, setTab] = useState<NoteTab>(initialTab);
    const [open, setOpen] = useState(initialOpen);
    const [note, setNote] = useState('첫 줄 메모\n둘째 줄');
    return createElement(SessionNoteEditor, {
      open,
      onOpenChange: setOpen,
      headerSlot: createElement('span', null, '분류 슬롯'),
      tab,
      onTabChange: setTab,
      session: { shortId: 'c8e41d27', title: '알림 센터에 필터와 읽음 처리 넣기' },
      sessionNote: note,
      onSessionNoteChange: (text: string) => {
        onSessionChange(text);
        setNote(text);
      },
      globalNote: '전체 메모',
      onGlobalNoteChange: noop,
      saveState,
      notes: [{ sessionId: 'c8e41d27-x', shortId: 'c8e41d27', title: '알림 센터', firstLine: '첫 줄 메모' }],
      onPickSession: onPick,
    });
  };

  it('탭은 이 세션 · 전체 · 목록이고 저장 상태를 글자로 말한다', () => {
    render(createElement(Harness));
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['이 세션', '전체', '목록']);
    expect(screen.getByRole('status').textContent).toBe('저장됨');
    cleanup();
    render(createElement(Harness, { saveState: 'dirty' }));
    expect(screen.getByRole('status').textContent).toBe('저장 안 됨');
  });

  it('접으면 머리 한 줄만 남고 탭과 글은 사라진다. 머리에 끼워 넣은 것은 그대로 보인다', async () => {
    render(createElement(Harness, { initialOpen: false }));
    expect(screen.queryByRole('tab')).toBeNull();
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(screen.getByText('분류 슬롯')).toBeTruthy();
    expect(screen.getByRole('status').textContent).toBe('저장됨');
    const toggle = screen.getByRole('button', { name: '메모' });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    await userEvent.click(toggle);
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(screen.getAllByRole('tab')).toHaveLength(3);
  });

  it('이 세션 탭: 글을 고치면 알린다(제어형)', async () => {
    const onSessionChange = vi.fn();
    render(createElement(Harness, { onSessionChange }));
    const box = screen.getByRole('textbox', { name: '이 세션의 메모' });
    expect((box as HTMLTextAreaElement).value).toBe('첫 줄 메모\n둘째 줄');
    await userEvent.type(box, '!');
    expect(onSessionChange).toHaveBeenLastCalledWith('첫 줄 메모\n둘째 줄!');
  });

  it('전체 탭과 목록 탭', async () => {
    const onPick = vi.fn();
    render(createElement(Harness, { onPick }));
    await userEvent.click(screen.getByRole('tab', { name: '전체' }));
    expect((screen.getByRole('textbox', { name: '전체 메모' }) as HTMLTextAreaElement).value).toBe('전체 메모');
    await userEvent.click(screen.getByRole('tab', { name: '목록' }));
    const list = screen.getByRole('list', { name: '메모가 있는 세션' });
    await userEvent.click(within(list).getByRole('button', { name: /첫 줄 메모/ }));
    expect(onPick).toHaveBeenCalledWith('c8e41d27-x');
  });

  it('비소유 범위는 안내다', () => {
    const view = render(
      createElement(SessionNoteEditor, {
        open: true,
        onOpenChange: noop,
        tab: 'session',
        onTabChange: noop,
        session: null,
        sessionNote: '',
        onSessionNoteChange: noop,
        globalNote: '',
        onGlobalNoteChange: noop,
        saveState: 'saved',
        notes: [],
        onPickSession: noop,
        unavailable: true,
      }),
    );
    expect(view.container.textContent).toContain('메모는 세션 주인에게만 제공됩니다');
    expect(screen.queryByRole('textbox')).toBeNull();
  });
});
