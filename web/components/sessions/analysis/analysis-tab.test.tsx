import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { A_ID, B_ID, C_ID, D_ID, E_ID, F_ID, G_ID, H_ID, I_ID, K_ID, M_ID, N_ID, O_ID, PREVIEW_SESSIONS, analysisFor, type SessionFixture } from '@/app/test/session-analysis/fixtures';
import { DETAIL_NOW } from '@/app/test/session-analysis/detail/builders';
import { flagship } from '@/app/test/session-analysis/detail/flagship';
import type { SessionAnalysis, SessionAnalysisFull, SessionRecord } from '@/lib/session-analysis-types';
import { SessionAnalysisTab } from './analysis-tab';
import { DetailsStrip } from './details-strip';
import { TooltipProvider } from '@/components/ui/tooltip';
import { isFullAnalysis } from './guards';
import { AnalysisUnavailable } from './state-notices';

const noop = () => undefined;

const render = (analysis: SessionAnalysis, props: { timeZone?: string; openTarget?: 'conversation' | 'waterfall' } = {}): string =>
  renderToStaticMarkup(createElement(SessionAnalysisTab, { analysis, now: DETAIL_NOW, onOpenRequest: noop, onOpenSession: noop, ...props }));

/** 사람이 읽는 글자만. */
const textOf = (html: string): string => html.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ');

const LEAKS = ['NaN', 'undefined', '[object Object]', 'Infinity'];

const flagshipFull = (): SessionAnalysisFull => {
  const a = flagship().analysis;
  if (!a || !isFullAnalysis(a)) throw new Error('flagship must be a full analysis');
  return a;
};

const passA = (id: string): SessionFixture => {
  const f = PREVIEW_SESSIONS.find((s) => s.id === id);
  if (!f) throw new Error(`pass A fixture ${id} missing`);
  return f;
};

const passAAnalysis = (id: string, role: 'owner' | 'admin' = 'owner'): SessionAnalysis => {
  const a = analysisFor(passA(id), role);
  if (!a) throw new Error(`no analysis for ${id}`);
  return a;
};

/** 패널 제목(h3)의 위치. 같은 낱말이 본문에 있어도 제목만 본다. */
const panelAt = (html: string, title: string): number => html.search(new RegExp(`<h3[^>]*>${title}</h3>`));

/** 한 패널의 마크업: 그 제목에서 다음 패널 제목 앞까지. */
const PANELS = ['세션 상태', '세션 성과', '세션 개요', '작업 상황', '턴 상세 14건', '플랜'];
const panelHtml = (html: string, title: string): string => {
  const start = panelAt(html, title);
  if (start < 0) throw new Error(`panel ${title} not rendered`);
  const later = PANELS.map((t) => panelAt(html, t)).filter((i) => i > start);
  return html.slice(start, later.length > 0 ? Math.min(...later) : html.length);
};

describe('선택한 세션의 실제 정보만 표시', () => {
  it('저장소의 규칙 판 목록과 태그는 세션에서 표시하지 않는다', () => {
    const html = render(flagshipFull());
    expect(panelAt(html, '규칙 파일 판')).toBe(-1);
    expect(textOf(html)).not.toMatch(/AGENTS\.md 판\s*v5/);
    expect(textOf(html)).not.toContain('세션이 실제로 읽은 판이라고 단정');
  });

  it('판정과 본문, 성과가 없는 응답은 빈 카드를 만들지 않는다', () => {
    const analysis = structuredClone(flagshipFull());
    delete analysis.judgement;
    delete analysis.signals;
    delete analysis.record;
    delete analysis.tasks;
    delete analysis.commits;
    analysis.recaps = { state: 'none', items: [] };
    analysis.outputs.edited_files = { state: 'observed', items: [], active_total: 0, abandoned_total: 0, truncated: false };
    analysis.outputs.prs = { items: [], truncated: false };
    analysis.outputs.lines = null;
    const html = render(analysis);
    for (const title of ['세션 상태', '세션 성과', '세션 개요', '작업 상황']) expect(panelAt(html, title), title).toBe(-1);
    expect(panelAt(html, '턴 상세 14건')).toBeGreaterThan(0);
    expect(textOf(html)).not.toContain('담겨 있지 않습니다');
  });

  it('본문 없는 개요 카드를 숨겨도 명시적인 재생성 동작은 조밀한 제어로 유지한다', () => {
    const analysis = structuredClone(flagshipFull());
    analysis.record = { state: 'failed', generated_at: null, covers_until: null };
    analysis.recaps = { state: 'none', items: [] };
    const html = renderToStaticMarkup(createElement(SessionAnalysisTab, {
      analysis, now: DETAIL_NOW, onOpenRequest: noop, onRegenerateRecord: noop,
    }));
    expect(panelAt(html, '세션 개요')).toBe(-1);
    expect(html).toContain('data-record-state="failed"');
    expect(textOf(html)).toContain('다시 만들기');
  });

  it('기록 본문이 있어도 작업 항목이 없으면 작업 카드는 만들지 않는다', () => {
    const analysis = structuredClone(flagshipFull());
    if (analysis.record?.state !== 'ready') throw new Error('ready record required');
    analysis.record.work = [];
    const html = render(analysis);
    expect(panelAt(html, '세션 개요')).toBeGreaterThan(0);
    expect(panelAt(html, '작업 상황')).toBe(-1);
  });
});

describe('선택한 시간대의 상세 머리와 턴', () => {
  it('대화와 워터폴 선택은 각 이동 대상을 표시한다', () => {
    const analysis = flagshipFull();
    const conversation = panelHtml(render(analysis, { openTarget: 'conversation' }), '작업 상황');
    const waterfall = panelHtml(render(analysis), '작업 상황');
    expect(conversation).toContain('title="대화에서 이 요청 보기"');
    expect(conversation).not.toContain('title="턴 워터폴에서 선택"');
    expect(waterfall).toContain('title="턴 워터폴에서 선택"');
    expect(panelHtml(render(analysis, { openTarget: 'conversation' }), '턴 상세 14건')).toContain('title="대화에서 이 요청 보기"');
    expect(panelHtml(render(analysis), '턴 상세 14건')).toContain('title="턴 워터폴에서 선택"');
  });
  it('프로젝트 이름을 모르면 해시로 표시하고 null을 노출하지 않는다', () => {
    const analysis = flagshipFull();
    analysis.header.project = { hash: 'project-hash', name: null };
    const text = textOf(renderToStaticMarkup(createElement(TooltipProvider, null, createElement(DetailsStrip, {
      id: 'project-details', header: analysis.header, ownerView: false, now: DETAIL_NOW,
    }))));
    expect(text).toContain('project-hash');
    expect(text).not.toContain('null');
  });
  it('뉴욕 시간대를 머리의 시작 시각과 턴 시각에 같이 적용한다', () => {
    const analysis = flagshipFull();
    analysis.header.started_at = '2026-12-08T05:20:00Z';
    analysis.turns[0].started_at = '2026-12-08T05:20:00Z';
    const text = textOf(render(analysis, { timeZone: 'America/New_York' }));
    expect(text).toContain('12/08 00:20 GMT-05:00에 시작');
    expect(text).toContain('00:20 GMT-05:00');
  });
});

describe('대표 세션: 레퍼런스의 순서와 내용', () => {
  const html = render(flagshipFull());
  const text = textOf(html);

  it('내용이 있는 패널은 상태 → 성과 → 개요 → 작업 → 턴 상세 → 플랜 순서다', () => {
    const order = PANELS.map((t) => panelAt(html, t));
    expect(order.every((i) => i >= 0)).toBe(true);
    expect([...order].sort((a, b) => a - b)).toEqual(order);
  });

  it('머리는 ID와 지표를 보존하고 독립 타임라인 토글을 제거한다', () => {
    const head = textOf(html.slice(0, html.indexOf('세션 상태')));
    expect(head).toContain('c8e41d27');
    expect(head).toContain('실행 중');
    expect(head).toContain('알림 센터에 필터와 읽음 처리 넣기');
    expect(head).toContain('탭 이름: Inbox filters and read state');
    expect(head).not.toContain('타임라인 보기');
  });

  it('머리 둘째 줄: 계정 칩, "HH:MM에 시작 · 마지막 활동 N분 전 (길이 N시간 N분) · 요청 N건", 지표 칩 셋', () => {
    const head = textOf(html.slice(0, html.indexOf('세션 상태')));
    expect(head).toContain('dahye.sub');
    expect(head).toMatch(/\d\d:\d\d에 시작/);
    expect(head).toContain('마지막 활동 6분 전');
    expect(head).toContain('(길이 5시간 13분)');
    expect(head).toContain('요청 14건');
    expect(head).toContain('26.3M tok');
    expect(head).toContain('$7.57');
    expect(head).toMatch(/ctx\s+30%/);
  });

  it('머리에 독립 타임라인 버튼이 없다', () => {
    expect(textOf(render(flagshipFull()))).not.toContain('타임라인 보기');
  });

  it('세션 상태: 검증 전 모델 작업 판정을 완료 결론으로 접지 않고 관측 검사로 보인다', () => {
    const panel = panelHtml(html, '세션 상태');
    const t = textOf(panel);
    expect(t).toContain('관측 검사');
    expect(t).not.toContain('작업 완료 판정');
    for (const label of ['턴 종료', '답 대기 없음', '백그라운드 작업 판단 불가', '커밋됨', '판정 검증 대기']) expect(t).toContain(label);
    const states = [...panel.matchAll(/data-check="([a-z_]+)" data-state="([a-z]+)"/g)].map((m) => `${m[1]}:${m[2]}`);
    expect(states).toEqual([
      'turn_ended:pass',
      'no_pending_reply:pass',
      'no_background:undeterminable',
      'committed:pass',
      'work_done:undeterminable',
    ]);
    expect(t).toContain('관측 신호 5');
  });

  it('칩은 키보드로 닿는 버튼이고 이름이 상태를 글자로 말한다', () => {
    expect(html).toMatch(/<button[^>]*aria-label="백그라운드 작업: 판단 불가 — 백그라운드 작업 판단 불가"/);
    expect(html).toMatch(/<button[^>]*aria-label="커밋: 통과"/);
  });

  it('세션 성과: 세 줄, 첫 줄만 펼쳐져 task 칩과 동작 아이콘이 보인다', () => {
    const panel = panelHtml(html, '세션 성과');
    const t = textOf(panel);
    expect(t).toContain('작업·이슈 갱신');
    expect(t).toContain('task-123');
    expect(t).toContain('issue-45');
    expect(t).toContain('커밋 10건');
    expect(t).toContain('변경한 파일 16건');
    expect(panel.match(/aria-label="동작: [^"]+"/g)).toEqual(['aria-label="동작: 추가·완료·수정·커밋 연결"', 'aria-label="동작: 추가·커밋 연결"']);
    // 접힌 줄의 내용(커밋 목록, 편집 파일 표)은 아직 그리지 않는다.
    expect(panel).not.toContain('aria-label="커밋 목록"');
    expect(t).not.toContain('filter-chips.tsx');
  });

  it('세션 개요: 굵은 한 줄, 인라인 코드와 커밋 칩이 붙은 글머리표, 검토 사항, 태그 줄', () => {
    const panel = panelHtml(html, '세션 개요');
    const t = textOf(panel);
    expect(t).toContain('알림 센터에 종류별 필터와 읽음 처리를 넣었다');
    expect(panel).toMatch(/<code[^>]*>inbox_reads<\/code>/);
    expect(panel).toMatch(/<code[^>]*>99\+<\/code>/);
    for (const short of ['441e4b2', 'cabab59', 'c5d9aa2', '4b2a56e', '75a8883', '3776802']) expect(t).toContain(short);
    expect(t).toContain('검토 사항');
    for (const prefix of ['결정:', '해석:', '미확인:']) expect(t).toContain(prefix);
    expect(t).toMatch(/종별\s*구현\s*상담/);
    expect(t).toMatch(/컴포넌트\s*Next\.js\s*Postgres/);
    expect(t).toMatch(/화면\s*알림 센터/);
    expect(t).toMatch(/관련\s*task-123\s*feature-107\s*issue-45\s*pr-231/);
    expect(t).not.toMatch(/AGENTS\.md 판\s*v5/);
    expect(t).toContain('기록 4분 전');
  });

  it('작업 상황: 열일곱 줄, 종류 이름표, "→ 결정", 커밋 칩, 메모, 하위 항목', () => {
    const panel = panelHtml(html, '작업 상황');
    const t = textOf(panel);
    expect(t).toContain('17건');
    expect((panel.match(/data-state="unknown"/g) ?? []).length).toBeGreaterThanOrEqual(17);
    expect(panel).not.toContain('data-state="done"');
    expect(t).toContain('unvalidated_model');
    for (const kind of ['상담', '의뢰', '추가 의뢰', '마무리']) expect(t).toContain(kind);
    expect(t).toContain('→ 결정: 필터 줄은 목록 바로 위에 두고');
    expect(t).toContain('441e4b2');
    expect(t).toContain('구분선은 그대로 두기로 함');
    expect(panel).toContain('aria-label="하위 항목"');
    expect(t).toContain('작업 칩에 role과 aria-pressed');
    expect(t).toContain('확인 스크린리더 낭독 순서를 확인했다');
  });

  it('턴 상세: "턴 상세 14건", 번호, 상태, 지표 칩, 질문 줄, 커밋 줄', () => {
    const panel = panelHtml(html, '턴 상세 14건');
    const t = textOf(panel);
    expect(t).toContain('1.');
    expect(t).toContain('끝남');
    expect(t).toContain('6.2M tok · $2.26 · ctx 18%');
    expect(t).toContain('질문: 표시 위치 / 읽음 저장');
    expect(t).toContain('feat(inbox): 종류별 필터 칩과 읽음 상태 모델을 추가');
    expect(t).toContain('docs(board): task-123에 커밋 해시를 기록');
    expect(panel.match(/title="턴 워터폴에서 선택"/g)).toHaveLength(14);
  });

  it('플랜은 턴 목록 뒤에 붙고 세션 주입을 증명하지 못하는 규칙 목록은 없다', () => {
    expect(textOf(panelHtml(html, '플랜'))).toContain('승인');
    expect(panelAt(html, '규칙 파일 판')).toBe(-1);
  });

  it('새는 값이 없다', () => {
    for (const leak of LEAKS) expect(text, leak).not.toContain(leak);
    expect(text).not.toMatch(/\bnull\b/);
  });
});

describe('세션 기록의 상태마다 화면이 있다', () => {
  const withRecord = (record: SessionRecord): string => render({ ...flagshipFull(), record });

  it('stale: 본문은 그대로 두고 어디까지 반영했는지 알린다', () => {
    const base = flagshipFull().record;
    if (!base || base.state !== 'ready') throw new Error('flagship record must be ready');
    const t = textOf(withRecord({ ...base, state: 'stale' }));
    expect(t).toContain('이 기록은');
    expect(t).toContain('까지의 대화만 반영합니다');
    expect(t).toContain('알림 센터에 종류별 필터와 읽음 처리를 넣었다');
  });

  it('공개 가능한 recap이 있는 생성 상태는 설명을 한 번만 표시하고 빈 작업 카드는 숨긴다', () => {
    const expected: Array<[SessionRecord['state'], string]> = [
      ['generating', '세션 기록을 만드는 중입니다'],
      ['none', '아직 세션 기록이 없습니다'],
      ['failed', '세션 기록 생성에 실패했습니다'],
    ];
    for (const [state, title] of expected) {
      const html = withRecord({ state: state as 'none', generated_at: null, covers_until: null });
      expect(textOf(panelHtml(html, '세션 개요')), state).toContain(title);
      expect(html, state).toContain(`data-record-state="${state}"`);
      expect(panelAt(html, '작업 상황'), state).toBe(-1);
    }
  });

  it('기록이 없을 때는 Claude Code recap이 개요 자리를 대신하되 가려진 상태는 대신하지 않는다', () => {
    const none = textOf(panelHtml(withRecord({ state: 'none', generated_at: null, covers_until: null }), '세션 개요'));
    expect(none).toContain('대신 보이는 값: Claude Code가 자리를 비운 뒤 남긴 recap');
    expect(none).toContain('낙관적 갱신과 가상 스크롤을 넣었습니다');
    for (const state of ['redacted'] as const) {
      const html = withRecord({ state, generated_at: null, covers_until: null });
      expect(panelAt(html, '세션 개요'), state).toBe(-1);
      expect(textOf(html)).not.toContain('대신 보이는 값');
    }
  });

  it('기록이 있으면 recap은 접어 둔다(개수만 보인다)', () => {
    const t = textOf(panelHtml(render(flagshipFull()), '세션 개요'));
    expect(t).toContain('Claude Code recap 2건');
    expect(t).not.toContain('낙관적 갱신과 가상 스크롤을 넣었습니다');
  });

  it('기록 객체가 없어도 실제 recap은 표시하고 빈 작업 카드는 만들지 않는다', () => {
    const { record, ...rest } = flagshipFull();
    void record;
    const t = textOf(render(rest));
    expect(t).toContain('이 응답에는 세션 기록이 담겨 있지 않습니다. 기록을 받지 못한 것이며, 기록이 없다는 뜻이 아닙니다');
    expect(t).not.toContain('이 응답에는 세션 기록이 담겨 있지 않아 작업 상황을 보일 수 없습니다');
    expect(t).toContain('낙관적 갱신과 가상 스크롤을 넣었습니다');
  });
});

describe('비소유 관리자', () => {
  const adminView = (): SessionAnalysisFull => {
    const a = flagshipFull();
    const { signals, live, judgement, note, classification, ...rest } = a;
    void signals;
    void live;
    void judgement;
    void note;
    void classification;
    const { category, ...header } = rest.header;
    void category;
    return {
      ...rest,
      header,
      owner_view: false,
      identities: { user_ids: ['usr-a21e5c'], profile_emails: ['seo.dahye@example.com'], login_emails: ['dahye.sub@example.com'], mixed: false },
    };
  };

  it('판정 권한이 없는 관리자에게 빈 상태 패널을 만들지 않는다', () => {
    const html = render(adminView());
    expect(panelAt(html, '세션 상태')).toBe(-1);
    expect(html).not.toContain('data-check=');
    expect(textOf(html.slice(0, html.indexOf('세션 성과')))).not.toContain('실행 중');
  });

  it('내용(기록, 커밋, task, 턴 상세)은 그대로 보인다', () => {
    const html = render(adminView());
    expect(textOf(panelHtml(html, '세션 개요'))).toContain('알림 센터에 종류별 필터와 읽음 처리를 넣었다');
    expect(textOf(panelHtml(html, '세션 성과'))).toContain('커밋 10건');
    expect(textOf(panelHtml(html, '턴 상세 14건'))).toContain('feat(inbox)');
  });
});

describe('패스 C 필드가 없는 응답(패스 A 픽스처)은 "받지 못함"으로 그린다', () => {
  it('판정을 받지 못한 응답은 판단 없는 상태 카드를 만들지 않는다', () => {
    const html = render(passAAnalysis(A_ID));
    expect(panelAt(html, '세션 상태')).toBe(-1);
    expect(textOf(html)).not.toContain('이 응답에는 작업 완료 판정이 담겨 있지 않습니다');
    expect(panelAt(html, '턴 상세 13건')).toBeGreaterThan(0);
  });

  it('성과 줄은 task와 커밋을 받지 못했다고 적고, 파일과 PR은 그대로 보인다', () => {
    const html = render(passAAnalysis(A_ID));
    const t = textOf(panelHtml(html, '세션 성과'));
    expect(t).toContain('작업·이슈 갱신을 받지 못함');
    expect(t).toContain('커밋을 받지 못함');
    expect(t).toContain('변경한 파일 12건');
  });

  it('범주 칩이 상태 칩 자리를 대신한다', () => {
    const t = textOf(render(passAAnalysis(A_ID)).slice(0, 4000));
    expect(t).toContain('응답 대기');
    expect(t).toContain('권한 승인');
  });
});

describe('0과 모름이 다르게 보인다', () => {
  it('턴 경계를 판단할 수 없는 세션: 요청은 "알 수 없음", 0이 아니다. 턴 목록은 만들지 않았다고 적는다', () => {
    const html = render(passAAnalysis(C_ID));
    const head = textOf(html.slice(0, html.indexOf('세션 상태')));
    expect(head).toMatch(/요청\s*알 수 없음/);
    expect(head).not.toMatch(/요청\s*0/);
    expect(textOf(html)).toContain('턴 목록을 만들지 않았습니다');
  });

  it('요청 0은 관측한 0이라 숫자로 적는다 (O)', () => {
    const head = textOf(render(passAAnalysis(O_ID)).slice(0, 3000));
    expect(head).toContain('요청 0건');
  });

  it('줄 수 스냅샷이 없으면 0이 아니라 "기록 없음"이다 (B)', () => {
    // 변경한 파일 줄을 펼쳐야 보이는 내용이라 정적 마크업에는 없다 — 펼침은 interactions 시험이 본다.
    const t = textOf(render(passAAnalysis(B_ID)));
    expect(t).not.toContain('+0');
  });

  it('경로를 가린 세션의 성과 줄은 파일 수가 아니라 "경로 가려짐"이다 (C)', () => {
    expect(textOf(panelHtml(render(passAAnalysis(C_ID)), '세션 성과'))).toContain('변경한 파일 경로 가려짐');
  });

  it('성과 자료가 없는 Codex 세션에 빈 성과 카드나 파일 0건을 표시하지 않는다 (D)', () => {
    const html = render(passAAnalysis(D_ID));
    expect(panelAt(html, '세션 성과')).toBe(-1);
    expect(textOf(html)).not.toContain('변경한 파일 0건');
  });
});

describe('응답의 상태마다 화면이 있다', () => {
  it('stale: 규칙 갱신 중 안내와 값', () => {
    const t = textOf(render(passAAnalysis(B_ID)));
    expect(t).toContain('분석 규칙 갱신 중');
    expect(t).toContain('문서 사이트 검색 인덱스 재생성 스크립트');
  });

  it('building: 안내 문구 없이 조용하다', () => {
    const t = textOf(render(passAAnalysis(F_ID)));
    expect(t).not.toContain('분석을 만드는 중');
    expect(t).not.toContain('만들어진 뒤에');
    expect(t).not.toContain('기다리는 중');
    expect(t).not.toContain('반영 대기');
    expect(t).not.toContain('제목 없음');
  });

  it('truncated: 합계만 보이고 계산하지 않은 것임을 말한다', () => {
    const html = render(passAAnalysis(G_ID));
    const t = textOf(html);
    expect(t).toContain('세션이 너무 커서 합계만 보입니다');
    expect(t).toContain('계산하지 않은 것');
    expect(t).toContain('세션이 너무 커서 제목을 읽지 않았습니다');
    expect(panelAt(html, '턴 상세')).toBe(-1);
  });

  it('unsupported: 지원하지 않는 에이전트 안내, 해당 없는 칸은 그리지 않는다', () => {
    const t = textOf(render(passAAnalysis(H_ID)));
    expect(t).toContain('이 에이전트는 분석을 지원하지 않습니다');
    expect(t).toContain('이 에이전트는 분석하지 않아 제목을 읽지 않습니다');
    expect(t).not.toContain('제목 없음');
    expect(t).not.toMatch(/요청 알 수 없음/);
  });

  it('none: 읽을 수 있는 분석이 없다', () => {
    expect(textOf(render(passAAnalysis(I_ID)))).toContain('읽을 수 있는 분석이 없습니다');
  });

  it('404(신원이 섞임)는 분석을 볼 수 없다는 안내다', () => {
    expect(textOf(renderToStaticMarkup(createElement(AnalysisUnavailable, { reason: 'mixed_identity' })))).toContain('여러 신원(사용자·프로필)의 기록이 섞여');
  });
});

describe('제목 상태', () => {
  it('redacted: 가렸다고 쓰고 첫 요청으로 대신하지 않는다 (C)', () => {
    const t = textOf(render(passAAnalysis(C_ID)));
    expect(t).toContain('제목 가려짐');
  });

  it('withheld: 첫 요청의 첫 줄과 "제목 보류됨(첫 요청)" (E)', () => {
    const t = textOf(render(passAAnalysis(E_ID)).slice(0, 4000));
    expect(t).toContain('제목 보류됨(첫 요청)');
    expect(t).toContain('락 대기 시간 초과 케이스 테스트를 추가해줘');
  });

  it('none: 제목 없음 (O)', () => {
    expect(textOf(render(passAAnalysis(O_ID)).slice(0, 3000))).toContain('제목 없음');
  });
});

describe('턴 상세: 패스 A의 상태 집합', () => {
  const html = render(passAAnalysis(A_ID));
  const panel = textOf(panelHtml(html, '턴 상세 13건'));

  it('상태 여섯과 출처 이름, 모드 배지, 반려, 이미지, 질문 머리글, PR', () => {
    for (const label of ['끝남', '중단', '오류', '응답 없음', '진행 중', '판단 불가']) expect(panel, label).toContain(label);
    for (const label of ['명령', '백그라운드 알림', '작업 중 입력', '플랜', '반려']) expect(panel, label).toContain(label);
    expect(panelHtml(html, '턴 상세 13건')).toContain('title="이미지 2개를 붙였습니다."');
    expect(panel).toContain('질문: 재현 범위');
    expect(panel).toContain('PR');
  });

  it('버려진 분기는 따로 모으고 세션 합계에 포함된다고 적는다', () => {
    expect(panel).toContain('버려진 분기 3턴');
    expect(panel).toContain('세션 합계에 포함');
    expect(panel).toContain('이벤트 소싱으로 바꿔서 구현부터 시작해줘');
  });

  it('값을 모르는 이유는 툴팁뿐이 아니라 목록 아래에 글자로도 적는다', () => {
    expect(panel).toContain('알 수 없는 값의 이유');
    expect(panel).toMatch(/비용 귀속 불가 2\s*턴/);
    expect(panel).toContain('턴 구분 정보가 없던 시기(P0 이전)');
    expect(panel).toContain('명령 턴과 그 뒤 입력 턴이 같은 prompt_id를 써서');
  });

  it('귀속하지 못한 서브에이전트 호출을 알린다', () => {
    expect(panel).toContain('서브에이전트 호출 6건');
  });

  it('긴 세션: 활성 100행·버려진 100행만 먼저 그린다', () => {
    const p = render(passAAnalysis('6f2d80c7-1e94-4b3a-8a57-d3c9e0b41a68'));
    expect(p.match(/title="턴 워터폴에서 선택"/g)).toHaveLength(200);
    expect(textOf(p)).toContain('1,900개 더 보기');
    expect(textOf(p)).toContain('턴이 많아 서버가 앞의 2,000개만 보냈습니다');
  });
});

describe('모든 픽스처가 깨지지 않고 그려진다', () => {
  for (const f of PREVIEW_SESSIONS) {
    for (const role of ['owner', 'admin'] as const) {
      const a = analysisFor(f, role);
      if (!a) continue;
      it(`${f.id.slice(0, 8)} (${role})`, () => {
        const t = textOf(render(a));
        expect(t.trim().length).toBeGreaterThan(60);
        for (const leak of LEAKS) expect(t, leak).not.toContain(leak);
        expect(t).not.toMatch(/\bnull\b/);
      });
    }
  }

  it('대표 세션은 두 보는 사람 모두에서 그려진다', () => {
    const a = flagshipFull();
    expect(textOf(render(a)).length).toBeGreaterThan(2000);
  });
});

// 시험이 쓰는 세션 id를 한곳에서 확인한다(픽스처가 바뀌어 id가 사라지면 이유를 알 수 있게).
describe('시험 전제', () => {
  it('쓰는 패스 A 픽스처가 모두 있다', () => {
    for (const id of [A_ID, B_ID, C_ID, D_ID, E_ID, F_ID, G_ID, H_ID, I_ID, K_ID, M_ID, N_ID, O_ID]) expect(PREVIEW_SESSIONS.some((f) => f.id === id), id).toBe(true);
  });
});
