import type { OtelEvent } from '@/lib/types';
import { EventRow } from './event-row';

interface EventsTableProps {
  events: OtelEvent[];
}

const EventsTable = ({ events }: EventsTableProps) => (
  <table className="w-full">
    <thead className="sticky top-0 bg-surface z-10">
      <tr className="border-b border-border">
        <th className="py-2 px-3 text-left text-[11px] font-medium text-ink-3">Time</th>
        <th className="py-2 px-3 text-left text-[11px] font-medium text-ink-3">Event</th>
        <th className="py-2 px-3 text-left text-[11px] font-medium text-ink-3">Model</th>
        <th className="py-2 px-3 text-left text-[11px] font-medium text-ink-3">Session</th>
        <th className="py-2 px-3 text-left text-[11px] font-medium text-ink-3">User</th>
        <th className="py-2 px-3 text-right text-[11px] font-medium text-ink-3">Input</th>
        <th className="py-2 px-3 text-right text-[11px] font-medium text-ink-3">Output</th>
        <th className="py-2 px-3 text-right text-[11px] font-medium text-ink-3">Cost</th>
        <th className="py-2 px-3 text-right text-[11px] font-medium text-ink-3">Age</th>
      </tr>
    </thead>
    <tbody>
      {events.map((e, i) => (
        <EventRow key={`${e.ts}-${i}`} event={e} />
      ))}
    </tbody>
  </table>
);

export { EventsTable };
