import type { OtelMetric } from '@/lib/types';
import { formatRelativeTime } from '@/lib/format';
import { formatTs } from './logs-helpers';

interface MetricsTableProps {
  metrics: OtelMetric[];
}

// OTLP metrics carry no id, so the key is the value tuple that identifies the datapoint.
// A duplicate tuple gets an occurrence suffix — rows that genuinely collide stay distinct
// without making the other rows' keys depend on their position in the list.
const metricKeys = (metrics: OtelMetric[]): string[] => {
  const seen = new Map<string, number>();
  return metrics.map((m) => {
    const base = [m.ts, m.metric_name, m.model ?? '', m.profile_email ?? '', m.session_id ?? '',
      m.value_double ?? '', m.value_int ?? ''].join('\u0000');
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    return n === 0 ? base : `${base}#${n}`;
  });
};

const MetricsTable = ({ metrics }: MetricsTableProps) => {
  const keys = metricKeys(metrics);
  return (
  <table className="w-full">
    <thead className="sticky top-0 bg-surface z-10">
      <tr className="border-b border-border">
        <th className="py-2 px-3 text-left text-[11px] font-medium text-ink-3">Time</th>
        <th className="py-2 px-3 text-left text-[11px] font-medium text-ink-3">Metric</th>
        <th className="py-2 px-3 text-left text-[11px] font-medium text-ink-3">Model</th>
        <th className="py-2 px-3 text-left text-[11px] font-medium text-ink-3">User</th>
        <th className="py-2 px-3 text-right text-[11px] font-medium text-ink-3">Value</th>
        <th className="py-2 px-3 text-left text-[11px] font-medium text-ink-3 font-mono">Session</th>
        <th className="py-2 px-3 text-right text-[11px] font-medium text-ink-3">Age</th>
      </tr>
    </thead>
    <tbody>
      {/* Keyed by the row's own identity, not its index: a metric arriving at the head
          shifts every index below it, and React would then reuse the previous row's DOM
          for a different metric. */}
      {metrics.map((m, i) => (
        <tr key={keys[i]} className="border-b border-border hover:bg-canvas transition-colors">
          <td className="py-2 px-3 text-[12px] text-ink-3 font-mono whitespace-nowrap">{formatTs(m.ts)}</td>
          <td className="py-2 px-3">
            <span className="text-[10px] font-medium px-1.5 py-0.5 rounded bg-cat-6/15 text-cat-6">
              {m.metric_name}
            </span>
          </td>
          <td className="py-2 px-3 text-[12px] text-ink-2">{m.model?.replace('claude-', '') || '-'}</td>
          <td className="py-2 px-3 text-[12px] text-ink-2">{m.profile_email?.split('@')[0] || '-'}</td>
          <td className="py-2 px-3 text-[12px] text-ink text-right tabular-nums font-medium">
            {m.value_double != null ? m.value_double.toFixed(4) : m.value_int != null ? m.value_int.toLocaleString() : '-'}
          </td>
          <td className="py-2 px-3 text-[12px] text-ink-2 font-mono">{m.session_id?.slice(0, 8) || '-'}</td>
          <td className="py-2 px-3 text-[12px] text-ink-3 text-right">{formatRelativeTime(m.ts)}</td>
        </tr>
      ))}
    </tbody>
  </table>
  );
};

export { MetricsTable, metricKeys };
