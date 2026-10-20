/**
 * 패스 C 미리보기(`/test/session-analysis/detail/`)의 합성 픽스처를 한곳에서 내놓는다. 서버가 없으므로 계약
 * (`lib/session-analysis-types`)대로 만든 값을 화면 컴포넌트에 먹이는 용도다. 제목, 경로, 요청 문장, 이메일(example.com)은
 * 모두 지어낸 것이고 실제 대화에서 가져온 것이 하나도 없다.
 *
 * 구성:
 *   대표 세션 하나(`flagship.ts`)     레퍼런스 밀도: 요청 14, 입력 15, 커밋 10, 변경 파일 16, 작업 항목 17
 *   변형 열셋(`variants.ts`)          판정의 조합, 세션 기록의 모든 상태, 커밋의 세 상태, 빈 task 목록, Codex
 *   패스 A 열여섯(`adapt.ts`)         패스 A가 보이는 상태를 새 모양으로 감싼 것(응답이 완전하지 않은 상태와 404 포함)
 * 프레임(`frame.ts`)은 계정 셋의 사용률과 창 겹침 차트, 저장소 상태다.
 */

import type { SessionAnalysis, TimelineMessage } from '@/lib/session-analysis-types';
import { isFullAnalysis } from '@/components/sessions/analysis/guards';
import type { SessionOverview } from '@/lib/types';
import { PREVIEW_SESSIONS } from '../fixtures';
import { adaptPassA } from './adapt';
import { DETAIL_NOW, adminAnalysis, adminRow, type DetailFixture } from './builders';
import { FLAGSHIP_ID, flagship } from './flagship';
import { FRAME_ACCOUNT_LABELS, frameFor } from './frame';
import { variantSessions } from './variants';

export { DETAIL_NOW, FLAGSHIP_ID, FRAME_ACCOUNT_LABELS, frameFor };
export type { DetailFixture };

/** 보는 사람이 세션 주인인가, 주인이 아닌 관리자인가. */
export type ViewerRole = 'owner' | 'admin';

/** 대표 세션, 변형, 패스 A 순서로 쓴 그대로다. 목록의 정렬은 미리보기가 한다. */
export const DETAIL_SESSIONS: DetailFixture[] = [flagship(), ...variantSessions(), ...PREVIEW_SESSIONS.map((f, i) => adaptPassA(f, i))];

/** 관리자에게는 판정·신호·범주·실시간·메모·분류가 가지 않는다(집계와 개인 값). */
export const rowFor = (f: DetailFixture, role: ViewerRole): SessionOverview => (role === 'owner' ? f.row : adminRow(f.row));

export const analysisFor = (f: DetailFixture, role: ViewerRole): SessionAnalysis | null => {
  if (f.analysis === null) return null;
  return role === 'owner' ? f.analysis : adminAnalysis(f.analysis, f.identities);
};

/** 대화 원문은 관리자도 읽을 수 있어 타임라인은 두 역할이 같다. */
export const timelineFor = (f: DetailFixture, role: ViewerRole): TimelineMessage[] => {
  void role;
  return f.timeline;
};

/** 메모가 있는 세션의 메모 전문. 첫 줄이 목록 카드에 보이는 `row.analysis.note`와 같다. */
export const SEED_NOTES: Readonly<Record<string, string>> = Object.fromEntries(
  DETAIL_SESSIONS.flatMap((f) => (f.analysis !== null && isFullAnalysis(f.analysis) && f.analysis.note ? [[f.id, f.analysis.note.text] as const] : [])),
);

/** 세션과 무관한 하나의 메모("전체" 탭). */
export const SEED_GLOBAL_NOTE =
  '이번 주 정리\n- 알림 센터 필터는 목요일까지 PR로 올린다\n- 릴리스 노트 스크립트는 팀에 공유한 뒤 문서에 링크한다\n- 정산 로그 보존 기간은 마이그레이션부터 쓴다';
