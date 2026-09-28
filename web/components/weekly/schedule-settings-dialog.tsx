'use client';

import { useState } from 'react';
import type { ChangeEvent } from 'react';
import { useMutation } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog';
import { setAIUserSchedule } from '@/lib/api';
import type { AIScheduleSource, AIUserSchedule, SetAIUserScheduleRequest } from '@/lib/types';
import { WEEKDAY_NAMES } from './schedule-line';

/** Minutes a firing may land on. Every minute would be a 60-item list for a
 *  choice nobody makes that finely. */
const MINUTE_STEPS = [0, 10, 20, 30, 40, 50];
const HOURS = Array.from({ length: 24 }, (_, hour) => hour);

const pad = (n: number): string => String(n).padStart(2, '0');

const selectClass =
  'rounded-[var(--r-sm)] border border-border bg-surface px-2 py-1 text-[13px] text-ink disabled:opacity-50';

interface ScheduleDraft {
  enabled: boolean;
  weekday: number;
  hour: number;
  minute: number;
}

/** What the user chose, saved with the zone they chose it in so the firing is
 *  read in that zone from then on. */
const scheduleRequest = (draft: ScheduleDraft, tz: string): SetAIUserScheduleRequest => ({ ...draft, tz });

/** Hands the schedule back to the administrator by clearing every field.
 *
 *  Copying today's values into the row instead would look the same today and be
 *  wrong tomorrow: it would freeze the current default as this user's own
 *  setting, and a later change by the administrator would never reach them.
 *
 *  The zone is kept, not cleared. It is not part of the administrator's default
 *  -- a firing is read in each user's own zone -- and dropping it would leave
 *  the row with an empty tz, which the scheduler reads as UTC. That moves the
 *  actual firing hours away from the time the screen prints. */
const followAdminRequest = (tz: string): SetAIUserScheduleRequest => ({
  enabled: null,
  weekday: null,
  hour: null,
  minute: null,
  tz,
});

interface ScheduleFieldsProps extends ScheduleDraft {
  /** Which layer set the weekday and time now in force. */
  whenSource: AIScheduleSource;
  onChange: (next: ScheduleDraft) => void;
}

/** The editable part of the dialog, kept free of data fetching so it can be
 *  checked as markup, the way the consent disclosure is. */
const ScheduleFields = ({ enabled, weekday, hour, minute, whenSource, onChange }: ScheduleFieldsProps) => {
  const handleEnabled = (event: ChangeEvent<HTMLInputElement>) =>
    onChange({ enabled: event.target.checked, weekday, hour, minute });
  const handleWeekday = (event: ChangeEvent<HTMLSelectElement>) =>
    onChange({ enabled, weekday: Number(event.target.value), hour, minute });
  const handleHour = (event: ChangeEvent<HTMLSelectElement>) =>
    onChange({ enabled, weekday, hour: Number(event.target.value), minute });
  const handleMinute = (event: ChangeEvent<HTMLSelectElement>) =>
    onChange({ enabled, weekday, hour, minute: Number(event.target.value) });

  return (
    <div className="space-y-3 text-[13px] text-ink-2">
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={enabled} onChange={handleEnabled} className="accent-brand" />
        자동 생성
      </label>
      <div className="flex flex-wrap items-center gap-2">
        <select className={selectClass} value={weekday} onChange={handleWeekday} disabled={!enabled} aria-label="요일">
          {WEEKDAY_NAMES.map((name, index) => (
            <option key={name} value={index}>
              {name}요일
            </option>
          ))}
        </select>
        <select className={selectClass} value={hour} onChange={handleHour} disabled={!enabled} aria-label="시">
          {HOURS.map((value) => (
            <option key={value} value={value}>
              {pad(value)}시
            </option>
          ))}
        </select>
        <select className={selectClass} value={minute} onChange={handleMinute} disabled={!enabled} aria-label="분">
          {MINUTE_STEPS.map((value) => (
            <option key={value} value={value}>
              {pad(value)}분
            </option>
          ))}
        </select>
      </div>
      {whenSource !== 'user' && (
        <p className="text-[12px] text-ink-3">지금은 관리자 기본값을 따릅니다. 바꾸면 이 계정에만 적용됩니다.</p>
      )}
    </div>
  );
};

interface ScheduleSettingsDialogProps {
  schedule: AIUserSchedule;
  /** The zone the screen is showing, used when the user has saved none. */
  timeZone: string;
  onClose: () => void;
  onSaved: () => void;
}

const ScheduleSettingsDialog = ({ schedule, timeZone, onClose, onSaved }: ScheduleSettingsDialogProps) => {
  const [draft, setDraft] = useState<ScheduleDraft>({
    enabled: schedule.enabled,
    weekday: schedule.weekday,
    hour: schedule.hour,
    minute: schedule.minute,
  });

  const save = useMutation({
    mutationFn: (req: SetAIUserScheduleRequest) => setAIUserSchedule(req),
    onSuccess: onSaved,
  });

  const handleOpenChange = (open: boolean) => {
    if (!open) onClose();
  };
  const handleSave = () => save.mutate(scheduleRequest(draft, schedule.tz || timeZone));
  const handleFollowAdmin = () => save.mutate(followAdminRequest(schedule.tz || timeZone));

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogContent showCloseButton={false} className="block max-w-[420px] space-y-4 border-border bg-surface">
        <DialogTitle className="text-[15px] font-semibold text-ink">주간 리포트 실행 설정</DialogTitle>
        <ScheduleFields {...draft} whenSource={schedule.when_source} onChange={setDraft} />
        {save.error && <p className="text-[12.5px] text-danger">저장하지 못했습니다. 다시 시도해 주세요.</p>}
        <footer className="flex justify-end gap-2">
          {schedule.when_source === 'user' && (
            <Button type="button" variant="outline" size="sm" onClick={handleFollowAdmin} disabled={save.isPending}>
              관리자 기본값 따르기
            </Button>
          )}
          <Button type="button" variant="outline" size="sm" onClick={onClose}>
            취소
          </Button>
          <Button type="button" size="sm" onClick={handleSave} disabled={save.isPending}>
            저장
          </Button>
        </footer>
      </DialogContent>
    </Dialog>
  );
};

export { followAdminRequest, ScheduleFields, scheduleRequest, ScheduleSettingsDialog };
