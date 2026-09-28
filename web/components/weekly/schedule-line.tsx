import { Button } from '@/components/ui/button';
import type { AIUserSchedule } from '@/lib/types';
import { formatReportTime } from './report-items';

/** Sunday first, as the API counts weekdays and as Go's time.Weekday does. */
const WEEKDAY_NAMES = ['일', '월', '화', '수', '목', '금', '토'];

const pad = (n: number): string => String(n).padStart(2, '0');

const whenLabel = (schedule: AIUserSchedule): string =>
  `매주 ${WEEKDAY_NAMES[schedule.weekday] ?? '?'}요일 ${pad(schedule.hour)}:${pad(schedule.minute)}`;

interface ScheduleLineProps {
  schedule: AIUserSchedule;
  timeZone: string;
  onOpenSettings: () => void;
}

/** Says when this week's report runs by itself, so a user learns one is coming
 *  without pressing anything.
 *
 *  A time the user has not chosen is marked as the administrator's, because
 *  "someone set this for everyone" and "I set this" lead to different decisions
 *  about whether to change it. */
const ScheduleLine = ({ schedule, timeZone, onOpenSettings }: ScheduleLineProps) => (
  <footer className="flex flex-wrap items-center justify-between gap-2 text-[12px] text-ink-3">
    <p className="tabular-nums">
      {schedule.enabled ? whenLabel(schedule) : '자동 생성 꺼짐'}
      {schedule.enabled && schedule.next_run && ` · 다음 ${formatReportTime(schedule.next_run, timeZone)}`}
      {schedule.when_source !== 'user' && ' · 관리자 기본값'}
    </p>
    <Button type="button" variant="outline" size="xs" onClick={onOpenSettings}>
      실행 설정
    </Button>
  </footer>
);

export { ScheduleLine, WEEKDAY_NAMES };
