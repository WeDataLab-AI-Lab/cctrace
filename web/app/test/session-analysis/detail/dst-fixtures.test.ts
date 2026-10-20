import { describe, expect, it } from 'vitest';
import { DST_DEMO } from '../calendar/dst-demo';
import { partsAt, zonedMidnight } from '@/components/sessions/calendar/time-zone';
import { isFullAnalysis } from '@/components/sessions/analysis/guards';
import { DST_DETAIL_SESSIONS } from './dst-fixtures';

const fullOf = (id: string) => {
  const fixture = DST_DETAIL_SESSIONS.find((item) => item.id === id);
  if (!fixture || !fixture.analysis || !isFullAnalysis(fixture.analysis)) throw new Error(`No DST detail for ${id}`);
  return { fixture, analysis: fixture.analysis };
};

describe('DST calendar detail fixtures', () => {
  it('all seven sessions have the same absolute range and navigable conversation inputs', () => {
    expect(DST_DETAIL_SESSIONS).toHaveLength(7);
    for (const calendar of DST_DEMO) {
      const { fixture, analysis } = fullOf(calendar.session_id);
      expect(fixture.row.start_time).toBe(calendar.range.start);
      expect(fixture.row.end_time).toBe(calendar.range.end);
      expect(analysis.header.started_at).toBe(calendar.range.start);
      expect(analysis.header.last_activity_at).toBe(calendar.range.end);
      expect(analysis.header.title).toEqual(calendar.title);
      expect(analysis.header.counts.requests).toBe(calendar.inputs.items.length);
      expect(analysis.header.counts.inputs).toBe(calendar.inputs.items.length);
      expect(analysis.turns.map((turn) => [turn.key, turn.started_at, turn.preview])).toEqual(
        calendar.inputs.items.map((input) => [input.turn_key, input.ts, input.preview]),
      );
      expect(analysis.inputs.map((input) => [input.turn_key, input.ts])).toEqual(
        calendar.inputs.items.map((input) => [input.turn_key, input.ts]),
      );
      expect(fixture.timeline.filter((message) => message.role === 'user').map((message) => [message.turn_key, message.ts])).toEqual(
        calendar.inputs.items.map((input) => [input.turn_key, input.ts]),
      );
      expect(analysis.turns.every((turn) => turn.status === 'undeterminable' && turn.status_reason === 'pre_collect')).toBe(true);
    }
  });

  it('an unobserved record, commit count, and completion check are not fabricated', () => {
    for (const { id } of DST_DETAIL_SESSIONS) {
      const { fixture, analysis } = fullOf(id);
      expect(analysis.record).toMatchObject({ state: 'none' });
      expect(analysis.commits).toMatchObject({ state: 'not_collected', total: null, items: [] });
      expect(analysis.header.cost).toMatchObject({ usd: null, source: 'none' });
      expect(analysis.judgement).toMatchObject({ status: 'unknown', bucket: null });
      expect(analysis.judgement?.checks.every((check) => check.state === 'undeterminable')).toBe(true);
      expect(fixture.row.analysis?.judgement).toMatchObject({ status: 'unknown', bucket: null });
    }
  });

  it('New York spring has 23 hours and skips 02:00 without moving UTC input instants', () => {
    const zone = 'America/New_York';
    const day = { year: 2026, month: 3, day: 8 };
    expect((zonedMidnight(zone, { ...day, day: 9 }) - zonedMidnight(zone, day)) / 3_600_000).toBe(23);
    const inputs = fullOf('dst-spring-over-the-gap').analysis.inputs;
    expect(inputs.map((input) => input.ts)).toEqual([
      '2026-03-08T05:30:00.000Z', '2026-03-08T06:30:00.000Z',
      '2026-03-08T07:10:00.000Z', '2026-03-08T07:35:00.000Z',
    ]);
    expect(inputs.map((input) => partsAt(zone, Date.parse(input.ts)).hour)).toEqual([0, 1, 3, 3]);
  });

  it('New York fall has 25 hours and two distinct 01:30 inputs an hour apart', () => {
    const zone = 'America/New_York';
    const day = { year: 2025, month: 11, day: 2 };
    expect((zonedMidnight(zone, { ...day, day: 3 }) - zonedMidnight(zone, day)) / 3_600_000).toBe(25);
    const inputs = fullOf('dst-fall-over-the-repeat').analysis.inputs;
    const first = Date.parse(inputs[1].ts);
    const second = Date.parse(inputs[2].ts);
    expect(second - first).toBe(3_600_000);
    expect(inputs.slice(1, 3).map((input) => input.ts)).toEqual(['2025-11-02T05:30:00.000Z', '2025-11-02T06:30:00.000Z']);
    expect([partsAt(zone, first).hour, partsAt(zone, second).hour]).toEqual([1, 1]);
    expect([partsAt(zone, first).minute, partsAt(zone, second).minute]).toEqual([30, 30]);
  });
});
