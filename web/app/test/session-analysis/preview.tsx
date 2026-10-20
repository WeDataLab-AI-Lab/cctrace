'use client';

import { DetailPreview } from './detail/preview';

/** 목록·캘린더·상세·타임라인은 같은 합성 데이터와 선택 상태를 공유한다. */
const SessionAnalysisPreview = () => <DetailPreview />;

export { SessionAnalysisPreview };
