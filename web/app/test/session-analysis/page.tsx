'use client';

/**
 * /test/session-analysis — 세션 상세 분석 미리보기(합성 데이터).
 *
 * `/test`와 같은 자리다: 인증이 필요한 대시보드 밖이고 API가 필요 없다. `/test` 쇼케이스와 같이 운영
 * 빌드에서는 열리지 않고 홈으로 돌려보낸다. 제품 안에 이 경로로 가는 링크는 없다.
 * 화면 컴포넌트는 components/sessions/analysis/, 픽스처는 이 폴더의 fixtures.ts 에 있다.
 */

import { useEffect, useSyncExternalStore } from 'react';
import { useRouter } from 'next/navigation';
import { SessionAnalysisPreview } from './preview';

const subscribeNothing = () => () => {};

/**
 * 서버 렌더에서는 false, 브라우저에서는 true. 시각과 날짜가 로케일에 따라 달라지는 화면(목록 행의 기간 등)을
 * 서버와 브라우저가 다르게 그려 하이드레이션이 어긋나는 것을 막는다. 실제 세션 화면은 데이터를 받은 뒤에야
 * 행을 그리므로 서버 렌더에 행이 없다 — 미리보기도 같은 조건으로 맞춘다.
 */
const useIsClient = (): boolean => useSyncExternalStore(subscribeNothing, () => true, () => false);

export default function SessionAnalysisPreviewPage() {
  const router = useRouter();
  const isClient = useIsClient();

  useEffect(() => {
    if (process.env.NODE_ENV === 'production') {
      router.replace('/');
    }
  }, [router]);

  if (process.env.NODE_ENV === 'production' || !isClient) {
    return null;
  }

  return <SessionAnalysisPreview />;
}
