/**
 * 합성 세션의 어휘. 모두 지어낸 이름이다(실제 프로젝트·사람·문장이 하나도 없다).
 * 한 주에 값이 열댓 개씩 나오도록 화면·컴포넌트는 프로젝트마다 몇 개씩 묶여 있고, 그 안에서 앞의 것이 훨씬 자주 나온다.
 */

import type { AnalysisProject } from '@/lib/session-analysis-types';

export interface ProjectVocab {
  project: AnalysisProject;
  weight: number;
  /** 앞의 것일수록 자주 나온다. */
  screens: readonly string[];
  components: readonly string[];
}

export const PROJECTS: readonly ProjectVocab[] = [
  {
    project: { hash: '-Users-dev-work-web-console', name: 'web-console' },
    weight: 22,
    screens: ['대시보드', '주문 관리', '고객 목록', '리포트', '설정'],
    components: ['웹', '디자인 시스템'],
  },
  {
    project: { hash: '-Users-dev-work-billing-api', name: 'billing-api' },
    weight: 20,
    screens: ['결제', '정산', '대시보드'],
    components: ['결제 모듈', 'API'],
  },
  {
    project: { hash: '-Users-dev-work-data-pipeline', name: 'data-pipeline' },
    weight: 14,
    screens: ['리포트', '대시보드'],
    components: ['배치', '수집기'],
  },
  {
    project: { hash: '-Users-dev-work-mobile-app', name: 'mobile-app' },
    weight: 12,
    screens: ['온보딩', '알림', '로그인', '프로필'],
    components: ['모바일', '알림 워커'],
  },
  {
    project: { hash: '-Users-dev-work-docs-site', name: 'docs-site' },
    weight: 10,
    screens: ['검색', '문서'],
    components: ['검색 인덱서', '웹'],
  },
  {
    project: { hash: '-Users-dev-work-notify-worker', name: 'notify-worker' },
    weight: 8,
    screens: ['알림', '설정'],
    components: ['알림 워커', 'API'],
  },
  {
    project: { hash: '-Users-dev-work-infra-tools', name: 'infra-tools' },
    weight: 8,
    screens: ['설정'],
    components: ['CLI', '배치'],
  },
  {
    project: { hash: '-Users-dev-work-design-tokens', name: 'design-tokens' },
    weight: 6,
    screens: ['설정', '대시보드'],
    components: ['디자인 시스템'],
  },
];

/** 종별(레퍼런스의 여섯: 구현, 조사, 정비, 상담, 버그 수정, 문서). */
export const KINDS: readonly { value: string; weight: number; suffix: string; task: string }[] = [
  { value: '구현', weight: 42, suffix: '추가', task: 'implementation' },
  { value: '버그 수정', weight: 20, suffix: '오류 수정', task: 'debugging' },
  { value: '조사', weight: 13, suffix: '원인 조사', task: 'review' },
  { value: '정비', weight: 12, suffix: '정리', task: 'operations' },
  { value: '상담', weight: 8, suffix: '방향 상담', task: 'planning' },
  { value: '문서', weight: 5, suffix: '문서화', task: 'documentation' },
];

/** 일감의 대상. 한국어 조사를 가리지 않는 문형에 끼워 쓴다. */
export const THINGS: readonly string[] = [
  '주문 목록 필터',
  '결제 취소 흐름',
  '재고 동기화',
  '알림 재전송',
  '로그인 세션 만료',
  '리포트 내보내기',
  '검색 색인 갱신',
  '배치 재시도 정책',
  '고객 목록 페이지네이션',
  '대시보드 차트 로딩',
  '설정 저장 버튼',
  '권한 검사',
  '온보딩 단계 이동',
  '토큰 갱신',
  '정산 합계 계산',
  '쿠폰 적용 순서',
  '이미지 업로드',
  '오프라인 큐',
  '다국어 문구',
  '접근성 라벨',
  '에러 바운더리',
  '캐시 무효화',
  '웹훅 서명 검증',
  '로그 샘플링',
  '환율 반올림',
  '야간 배치 시간대',
  '푸시 알림 묶음',
  '프로필 사진 자르기',
  '검색어 자동 완성',
  '디자인 토큰 이름',
];

/** 요청 첫 줄의 문형. `{t}`가 일감의 대상이다. */
export const PROMPTS: Record<string, readonly string[]> = {
  구현: ['{t} 추가해줘', '{t} 기능 만들어줘', '{t} 쪽에 새 옵션 붙여줘', '{t} 구현 이어서 해줘', '{t} 테스트도 같이 작성해줘'],
  '버그 수정': ['{t}에서 에러가 나는 원인 찾아줘', '{t} 이상하게 동작하는데 고쳐줘', '{t} 재현 조건 정리하고 수정해줘', '{t} 회귀 테스트 추가해줘'],
  조사: ['{t} 어떻게 되어 있는지 알아봐줘', '{t} 관련 코드 위치부터 찾아줘', '{t} 영향 범위 조사해줘'],
  정비: ['{t} 코드 정리해줘', '{t} 이름과 폴더 구조 다듬어줘', '{t} 중복 제거해줘'],
  상담: ['{t} 어떻게 잡는 게 좋을지 의견 줘', '{t} 두 가지 방식 중 뭐가 나을까', '{t} 방향 먼저 정하자'],
  문서: ['{t} 설명 문서 정리해줘', '{t} 사용법 README에 추가해줘'],
};

/** 이어 하는 말(작업 도중의 짧은 입력). */
export const FOLLOW_UPS: readonly string[] = [
  '좋아, 계속 진행해줘',
  '테스트도 돌려봐',
  '방금 거 다시 확인해줘',
  '그 부분은 건드리지 말고',
  '커밋 메시지는 짧게',
  '로그 레벨은 debug로 유지해줘',
  '다른 방식도 비교해줘',
  '이름은 지금 쪽이 낫다',
  '남은 항목만 마저 해줘',
  '타입 오류부터 해결해줘',
];

export const PROFILES: readonly { name: string; loginEmail: string }[] = [
  { name: '메인', loginEmail: 'main.account@example.com' },
  { name: '서브', loginEmail: 'sub.account@example.com' },
  { name: 'Codex', loginEmail: 'codex.account@example.com' },
];

export const CLAUDE_MODELS: readonly { value: string; weight: number }[] = [
  { value: 'claude-opus-5', weight: 55 },
  { value: 'claude-sonnet-5', weight: 35 },
  { value: 'claude-haiku-4-5', weight: 10 },
];

export const CODEX_MODELS: readonly { value: string; weight: number }[] = [
  { value: 'gpt-6-astra', weight: 60 },
  { value: 'gpt-6-sol', weight: 40 },
];

export const BRANCHES: readonly string[] = ['main', 'feature/filter-bar', 'fix/retry-edge', 'chore/cleanup', 'feature/export', 'fix/session-expiry'];
