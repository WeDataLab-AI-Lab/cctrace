/**
 * 캘린더 화면의 한국어 문구. 값을 모르는 것은 0이 아니라 "알 수 없음"이다(분석 화면과 같은 원칙).
 */

/** 0 = 일요일 ... 6 = 토요일 (`DayColumn.weekday`). */
const WEEKDAY_LABEL: readonly string[] = ['일', '월', '화', '수', '목', '금', '토'];

const CALENDAR_TEXT = {
  region: '주간 세션 캘린더',
  today: '이번 주',
  prev: '이전 주',
  next: '다음 주',
  zoomIn: '확대',
  zoomOut: '축소',
  zoomReset: '표준',
  reload: '다시 읽기',
  colorBy: '색상 기준',
  sessions: '세션',
  loading: '캘린더를 읽는 중입니다',
  emptyWeek: '이 주에는 활동한 세션이 없습니다',
  emptyWeekHint: '필터를 풀거나 다른 주로 옮겨 보세요.',
  game: '블록 깨기',
  summaryFresh: '개요는 최신',
  summaryStale: '개요 다시 만들기',
  summaryWriting: '개요를 쓰는 중…',
} as const;

/** The API reports the most recent completed range request, not the currently displayed range. */
const summaryWrittenText = (count: number): string => `마지막 범위 요청에서 ${count.toLocaleString('en-US')}건을 썼습니다`;

/** 전환일 열 머리의 길이 표기: "23시간", "25시간", 30분 전환은 "23.5시간". */
const dayLengthLabel = (hours: number): string => `${Number.isInteger(hours) ? hours : hours.toFixed(1)}시간`;

const dayLengthHint = (hours: number): string =>
  hours < 24
    ? `이 날은 시계가 건너뛰어 ${dayLengthLabel(hours)}입니다. 건너뛴 시각은 눈금에 없습니다.`
    : `이 날은 시계가 되돌아가 ${dayLengthLabel(hours)}입니다. 반복된 시각은 UTC 오프셋과 함께 두 번 있습니다.`;

export { WEEKDAY_LABEL, CALENDAR_TEXT, summaryWrittenText, dayLengthLabel, dayLengthHint };
