import type { CSSProperties } from 'react';
import { useState } from 'react';
import {
  PieChart,
  Pie,
  Cell,
  Tooltip,
  ResponsiveContainer,
} from 'recharts';
import { isFable, isAstra, modelColor } from '@/lib/colors';
import { cn } from '@/lib/utils';
import { ChartSkeleton } from '@/components/common/chart-skeleton';
import type { ViewMode } from '@/components/common/trend-chart';
import { fmt } from './overview-helpers';
import type { ModelDatum } from './overview-helpers';

interface ModelPieTooltipProps {
  active?: boolean;
  payload?: readonly { name?: string | number; value?: number | string }[];
  viewMode: ViewMode;
  otherNames: string[];
}

// Custom content: the default Recharts tooltip colors item text with the cell fill,
// which for flagship gradients is url(#...) — invalid as a CSS color, so it fell back
// to ink (black). Render the name in a solid modelColor anchor instead.
const ModelPieTooltip = ({ active, payload, viewMode, otherNames }: ModelPieTooltipProps) => {
  if (!active || !payload || payload.length === 0) return null;

  const name = String(payload[0].name ?? '');
  if (!name) return null;

  const value = viewMode === 'token' ? fmt(Number(payload[0].value)) : `$${Number(payload[0].value).toFixed(4)}`;
  const label = name === 'Others' && otherNames.length > 0 ? `Others (${otherNames.join(', ')})` : name;
  const fable = isFable(name);
  const astra = isAstra(name);

  return (
    <div className="max-h-[60vh] max-w-[calc(100vw-2rem)] overflow-auto rounded-[4px] border border-border bg-surface px-2 py-1 text-[11px] [overflow-wrap:anywhere] shadow-[var(--sh-pop)]">
      <p className="m-0 flex items-center gap-1.5">
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
          style={fable || astra ? undefined : ({ '--tooltip-color': modelColor(name) } as CSSProperties)} // eslint-disable-line no-restricted-syntax
        />
        <span
          className="[color:var(--tooltip-color)]"
          style={({ '--tooltip-color': modelColor(name) } as CSSProperties)} // eslint-disable-line no-restricted-syntax
        >
          {label} : {value}
        </span>
      </p>
    </div>
  );
};

interface ModelPieChartProps {
  viewMode: ViewMode;
  data: ModelDatum[];
  otherNames: string[];
  isLoading?: boolean;
}

const ModelPieChart = ({ viewMode, data, otherNames, isLoading = false }: ModelPieChartProps) => {
  const title = viewMode === 'token' ? 'Tokens by Model' : 'Cost by Model';
  const [otherNamesOpen, setOtherNamesOpen] = useState(false);
  const handleOtherNamesToggle = () => setOtherNamesOpen((open) => !open);


  const renderLegend = () => (
    <div className="mt-4 flex flex-wrap justify-center gap-4 text-[12px] text-ink-3">
      {data.map((d) => {
        const fable = isFable(d.name);
        const astra = isAstra(d.name);

        return (
        <span
          key={d.name}
          className="group relative inline-flex min-w-0 max-w-full items-center gap-1"
        >
          <svg width={10} height={10} aria-hidden className="inline-block shrink-0">
            <rect width={10} height={10} fill={modelColor(d.name)} />
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
            style={fable || astra ? undefined : ({ '--legend-color': modelColor(d.name) } as CSSProperties)} // eslint-disable-line no-restricted-syntax
          >
            {d.name}
          </span>
          {d.name === 'Others' && otherNames.length > 0 && (
            <><button type="button" aria-label="Others 상세" aria-expanded={otherNamesOpen} onClick={handleOtherNamesToggle} className="min-h-11 rounded px-1 underline-offset-2 hover:underline lg:min-h-0">상세</button>
            <span className={cn('absolute bottom-full left-0 z-10 mb-0.5 max-w-[calc(100vw-2rem)] lg:left-1/2 lg:-translate-x-1/2', otherNamesOpen ? 'block' : 'hidden group-hover:block')}>
              <span className="block max-h-40 overflow-y-auto rounded border border-border bg-surface px-2 py-1 text-[11px] text-ink-3 [overflow-wrap:anywhere] shadow-[var(--sh-pop)]">
                {otherNames.join(', ')}
              </span>
              <span className="mx-auto block h-2 w-px bg-border" />
            </span></>
          )}
        </span>
        );
      })}
    </div>
  );

  return (
    <article className="flex min-w-0 flex-col rounded-xl border border-border bg-surface p-4 shadow-[var(--sh-sm)] sm:p-6">
      <h3 className="text-[14px] font-semibold text-ink mb-4">{title}</h3>
      {isLoading ? (
        <ChartSkeleton variant="pie" className="h-[240px]" />
      ) : data.length === 0 ? (
        <p className="text-sm text-ink-3 text-center py-8">No data</p>
      ) : (
        // The card's height is set by its neighbour, which grows a row per user --
        // twelve of them left the donut adrift in the middle of it. min-h keeps it
        // from collapsing in a short card; the radii are percentages so it fills
        // whatever height it is given instead of staying at 68px forever.
        <div className="flex min-h-[200px] flex-1 flex-col items-center justify-center">
          <ResponsiveContainer width="100%" height="100%">
            <PieChart>
              <Pie
                data={data}
                cx="50%"
                cy="50%"
                innerRadius="58%"
                outerRadius="88%"
                paddingAngle={3}
                startAngle={90}
                endAngle={-270}
                dataKey="value"
                nameKey="name"
                isAnimationActive={false}
              >
                {data.map((entry, i) => (
                  <Cell key={i} fill={modelColor(entry.name)} />
                ))}
              </Pie>
              <Tooltip
                content={<ModelPieTooltip viewMode={viewMode} otherNames={otherNames} />}
                wrapperStyle={{ zIndex: 50 }}
                isAnimationActive={false}
              />
            </PieChart>
          </ResponsiveContainer>
          {renderLegend()}
        </div>
      )}
    </article>
  );
};

export { ModelPieChart };
