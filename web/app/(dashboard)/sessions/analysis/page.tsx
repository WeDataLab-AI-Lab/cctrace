'use client';

import { Suspense, useEffect } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { sessionAnalysisCompatibilityUrl } from '@/lib/session-detail-navigation';

const AnalysisCompatibilityRedirect = () => {
  const router = useRouter();
  const params = useSearchParams().toString();
  /** 이전 독립 분석 URL의 선택과 범위를 새 인라인 탭으로 옮긴다. */
  useEffect(() => {
    router.replace(sessionAnalysisCompatibilityUrl(new URLSearchParams(params)), { scroll: false });
  }, [router, params]);
  return <p role="status" className="p-4 text-sm text-ink-3">세션 기록검사로 이동합니다.</p>;
};

export default function SessionAnalysisPage() {
  return <Suspense fallback={<p role="status" className="p-4 text-sm text-ink-3">세션 기록검사로 이동합니다.</p>}><AnalysisCompatibilityRedirect /></Suspense>;
}
