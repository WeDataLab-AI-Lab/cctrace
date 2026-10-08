import type { CSSProperties } from 'react';
import {
  BarChart,
  Bar,
  BarStack,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
} from 'recharts';
import { isFable, isAstra, modelColor } from '@/lib/colors';
import { WEEKLY_USAGE_HINT, isWeeklyUsageKey } from '@/lib/weekly-usage';
import { cn } from '@/lib/utils';
import { ChartSkeleton } from '@/components/common/chart-skeleton';
import type { ViewMode } from '@/components/common/trend-chart';
import { fmt } from './overview-helpers';
import type { UserStackDatum } from './overview-helpers';

interface UserStackChartProps {
  viewMode: ViewMode;
  data: UserStackDatum[];
  stackKeys: string[];
  isLoading?: boolean;
}

interface TooltipPayloadItem {
  name?: string | number;
  dataKey?: string | number;
  value?: number | string | ReadonlyArray<number | string>;
}

interface UserStackTooltipProps {
  active?: boolean;
  payload?: readonly TooltipPayloadItem[];
  viewMode: ViewMode;
}

const BAR_STACK_RADIUS: [number, number, number, number] = [0, 3, 3, 0];

const numericTooltipValue = (value: TooltipPayloadItem['value']): number => {
  if (Array.isArray(value)) return Number(value[1] ?? 0) - Number(value[0] ?? 0);
  return Number(value ?? 0);
};

const UserStackTooltip = ({ active, payload, viewMode }: UserStackTooltipProps) => {
  if (!active || !payload) return null;

  const items = payload
    .map((item) => ({
      name: String(item.name ?? item.dataKey ?? ''),
      value: numericTooltipValue(item.value),
    }))
    .filter((item) => item.name !== '' && item.value > 0)
    .sort((a, b) => b.value - a.value);

  if (items.length === 0) return null;

  return (
    <div className="rounded-[4px] border border-border bg-surface px-2 py-1 text-[11px] shadow-[var(--sh-pop)]">
      {items.map((item) => {
        const fable = isFable(item.name);
        const astra = isAstra(item.name);
        const value = viewMode === 'token' ? fmt(item.value) : `$${item.value.toFixed(2)}`;

        return (
          <p key={item.name} className="m-0 flex items-center gap-1.5">
            <span
              aria-hidden
              className={cn(
                'h-2 w-2 shrink-0 rounded-[2px]',
                fable
                  ? 'bg-model-fable'
                  : astra
                    ? 'bg-model-astra'
                    : 'bg-[color:var(--tooltip-color)]',
              )}
              style={fable || astra ? undefined : ({ '--tooltip-color': modelColor(item.name) } as CSSProperties)} // eslint-disable-line no-restricted-syntax
            />
            {/* Solid color for every model incl. flagship gradients: gradient bg-clip-text text is
                unreliable in the constantly-repositioned tooltip (Chromium repaints it
                black). modelColor resolves to the solid anchor token. */}
            <span
              className="[color:var(--tooltip-color)]"
              style={({ '--tooltip-color': modelColor(item.name) } as CSSProperties)} // eslint-disable-line no-restricted-syntax
            >
              {item.name} : {value}
            </span>
          </p>
        );
      })}
    </div>
  );
};

// Keep the wrapping legend outside Recharts so its measured height cannot shrink
// the user rows or mix viewport dimensions with chart coordinates under zoom.
const UserStackLegend = ({ stackKeys }: { stackKeys: string[] }) => (
    <div className="mt-4 flex flex-wrap justify-center gap-4 text-[11px] text-ink-3">
      {stackKeys.map((key) => {
        const fable = isFable(key);
        const astra = isAstra(key);

        return (
          <span key={key} className="inline-flex min-w-0 max-w-full items-center gap-1">
            <svg width={8} height={8} aria-hidden className="inline-block shrink-0">
              <rect width={8} height={8} fill={modelColor(key)} />
            </svg>
            <span
              className={cn(
                'min-w-0 [overflow-wrap:anywhere]',
                fable
                  ? 'text-model-fable'
                  : astra
                    ? 'text-model-astra'
                    : '[color:var(--legend-color)]',
              )}
              // CSS variable drives the per-model legend text color (Tailwind cannot express dynamic values)
              style={fable || astra ? undefined : ({ '--legend-color': modelColor(key) } as CSSProperties)} // eslint-disable-line no-restricted-syntax
            >
              {key}
            </span>
          </span>
        );
      })}
    </div>
);

interface UserAxisTickProps {
  x?: number | string;
  y?: number | string;
  payload?: { value?: string | number };
}

const WEEKLY_TICK_HINT_ID = 'user-stack-chart-weekly-hint';

// The weekly report line is a row here like any person's, so its name carries the
// explanation as a native SVG hover title -- the axis is SVG, not HTML. The tick is
// focusable and names itself, so the title reaches a screen reader as a description
// rather than replacing the label.
const UserAxisTick = ({ x, y, payload }: UserAxisTickProps) => {
  const label = String(payload?.value ?? '');
  if (!isWeeklyUsageKey(label)) {
    return (
      <text x={x} y={y} dy={4} textAnchor="end" fontSize={12} className="fill-ink-3">
        {label}
      </text>
    );
  }
  return (
    <text
      x={x}
      y={y}
      dy={4}
      textAnchor="end"
      fontSize={12}
      className="fill-ink-3"
      tabIndex={0}
      aria-label={label}
      aria-describedby={WEEKLY_TICK_HINT_ID}
    >
      <title id={WEEKLY_TICK_HINT_ID}>{WEEKLY_USAGE_HINT}</title>
      {label}
    </text>
  );
};

const UserStackChart = ({ viewMode, data, stackKeys, isLoading = false }: UserStackChartProps) => {
  const title = viewMode === 'token' ? 'Tokens by User' : 'Cost by User';
  const yAxisFmt = (v: number): string => (viewMode === 'token' ? fmt(v) : `$${v}`);


  return (
    <article className="min-w-0 bg-surface rounded-xl border border-border shadow-[var(--sh-sm)] p-6">
      <h3 className="text-[14px] font-semibold text-ink mb-4">{title}</h3>
      {isLoading ? (
        <ChartSkeleton variant="bar" className="h-[280px]" />
      ) : data.length === 0 ? (
        <p className="text-sm text-ink-3 text-center py-8">No data</p>
      ) : (
        <>
          <ResponsiveContainer width="100%" height={Math.max(200, data.length * 40)}>
            <BarChart data={data} layout="vertical" margin={{ left: 20 }}>
              {/* Flagship gradients share CSS stop tokens so palette tweaks stay in sync. */}
              <CartesianGrid strokeDasharray="3 3" stroke="var(--border-subtle)" />
              <XAxis type="number" tick={{ fontSize: 11 }} tickFormatter={yAxisFmt} />
              <YAxis type="category" dataKey="name" tick={<UserAxisTick />} width={100} interval={0} />
              <Tooltip
                content={<UserStackTooltip viewMode={viewMode} />}
                wrapperStyle={{ zIndex: 50 }}
                isAnimationActive={false}
              />
              <BarStack stackId="a" radius={BAR_STACK_RADIUS}>
                {stackKeys.map((key) => (
                  <Bar
                    key={key}
                    dataKey={key}
                    stackId="a"
                    fill={modelColor(key)}
                    isAnimationActive={false}
                  />
                ))}
              </BarStack>
            </BarChart>
          </ResponsiveContainer>
          <UserStackLegend stackKeys={stackKeys} />
        </>
      )}
    </article>
  );
};

export { UserStackChart };
