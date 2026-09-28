'use client';

import Link from 'next/link';
import { useState } from 'react';
import { ChevronDown } from 'lucide-react';
import { Pie, PieChart, ResponsiveContainer, Tooltip } from 'recharts';
import { ChartSkeleton } from '@/components/common/chart-skeleton';
import { userColor } from '@/lib/colors';
import { projectAggregateHref, projectAggregateKey, projectAggregateLabel } from '@/lib/project-aggregate';
import type { Project, WeeklyInsightProject } from '@/lib/types';
import { cn } from '@/lib/utils';

// Seven named slices and an eighth for the rest: the categorical palette has eight
// colours, and a ninth would repeat one and read as the same project.
const NAMED_SLICES = 7;
const OTHERS_LABEL = '기타';

const numberFormat = new Intl.NumberFormat('en-US');

interface ProjectShareRow {
  key: string;
  label: string;
  href: string | null;
  sessionCount: number;
  share: string;
}

interface ProjectSlice extends ProjectShareRow {
  color: string;
}

interface ProjectOthers {
  sessionCount: number;
  share: string;
  color: string;
  projects: ProjectShareRow[];
}

interface ProjectSlices {
  slices: ProjectSlice[];
  others: ProjectOthers | null;
  total: number;
}

const formatShare = (count: number, total: number) => (total > 0 ? `${Math.round((count / total) * 100)}%` : '0%');

/** Session shares per project, top seven plus "기타".
 *
 *  Sessions and not tokens: Claude and Codex count input differently, so a token
 *  sum across agents is not a quantity (#671). Eight projects stay eight slices --
 *  a "기타" holding one project would hide a name to save no room. */
const buildProjectSlices = (projects: readonly WeeklyInsightProject[], registry: readonly Project[]): ProjectSlices => {
  const total = projects.reduce((sum, p) => sum + p.session_count, 0);
  const seen = new Map<string, number>();
  const rows = [...projects]
    .sort((a, b) => b.session_count - a.session_count)
    .map((p): ProjectShareRow => {
      // Two aggregate rows can resolve to one registry project; the key still has
      // to be unique for the list.
      const baseKey = projectAggregateKey(p, registry);
      const n = seen.get(baseKey) ?? 0;
      seen.set(baseKey, n + 1);
      return {
        key: n === 0 ? baseKey : `${baseKey}#${n}`,
        label: projectAggregateLabel(p, registry),
        href: projectAggregateHref(p),
        sessionCount: p.session_count,
        share: formatShare(p.session_count, total),
      };
    });

  const fold = rows.length > NAMED_SLICES + 1;
  const named = fold ? rows.slice(0, NAMED_SLICES) : rows;
  const rest = fold ? rows.slice(NAMED_SLICES) : [];
  const restSessions = rest.reduce((sum, r) => sum + r.sessionCount, 0);

  return {
    slices: named.map((row, i) => ({ ...row, color: userColor(i) })),
    others: fold
      ? { sessionCount: restSessions, share: formatShare(restSessions, total), color: userColor(NAMED_SLICES), projects: rest }
      : null,
    total,
  };
};

interface DonutDatum {
  key: string;
  name: string;
  value: number;
  fill: string;
}

interface DonutTooltipProps {
  active?: boolean;
  payload?: readonly { name?: string | number; value?: number | string }[];
}

const DonutTooltip = ({ active, payload }: DonutTooltipProps) => {
  if (!active || !payload || payload.length === 0) return null;
  return (
    <p className="m-0 rounded-[4px] border border-border bg-surface px-2 py-1 text-[11px] text-ink-2 shadow-[var(--sh-pop)]">
      {String(payload[0].name ?? '')} : 세션 {numberFormat.format(Number(payload[0].value))}개
    </p>
  );
};

const Swatch = ({ color }: { readonly color: string }) => (
  <svg width={10} height={10} aria-hidden className="shrink-0">
    <rect width={10} height={10} rx={2} fill={color} />
  </svg>
);

const RowFigures = ({ sessionCount, share }: { readonly sessionCount: number; readonly share: string }) => (
  <span className="ml-auto shrink-0 tabular-nums text-ink-3">
    {numberFormat.format(sessionCount)} · {share}
  </span>
);

const ProjectRow = ({ row, color }: { readonly row: ProjectShareRow; readonly color?: string }) => {
  const content = (
    <>
      {color ? <Swatch color={color} /> : <span aria-hidden className="w-2.5 shrink-0" />}
      <span className="truncate text-ink">{row.label}</span>
      <RowFigures sessionCount={row.sessionCount} share={row.share} />
    </>
  );
  const rowClass = 'flex items-center gap-2 rounded-sm px-2 py-1.5';

  if (!row.href) return <div className={rowClass}>{content}</div>;
  return (
    <Link href={row.href} className={cn(rowClass, 'hover:bg-surface-sunk')}>
      {content}
    </Link>
  );
};

const OthersRow = ({ others }: { readonly others: ProjectOthers }) => {
  const [expanded, setExpanded] = useState(false);
  const handleToggle = () => setExpanded((open) => !open);

  return (
    <li>
      <button
        type="button"
        aria-expanded={expanded}
        onClick={handleToggle}
        className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left hover:bg-surface-sunk"
      >
        <Swatch color={others.color} />
        <span className="text-ink">
          {OTHERS_LABEL} {others.projects.length}개
        </span>
        <ChevronDown size={14} aria-hidden className={cn('text-ink-3 transition-transform', expanded && 'rotate-180')} />
        <RowFigures sessionCount={others.sessionCount} share={others.share} />
      </button>
      {expanded && (
        <ul className="ml-4 border-l border-border-subtle pl-1">
          {others.projects.map((row) => (
            <li key={row.key}>
              <ProjectRow row={row} />
            </li>
          ))}
        </ul>
      )}
    </li>
  );
};

interface ProjectShareDonutProps {
  projects: readonly WeeklyInsightProject[];
  registry: readonly Project[];
  loading: boolean;
  registryError: boolean;
}

const EmptyLine = ({ children }: { readonly children: React.ReactNode }) => (
  <p className="py-4 text-[12.5px] text-ink-3">{children}</p>
);

const ProjectShareBody = ({ projects, registry, loading, registryError }: ProjectShareDonutProps) => {
  if (registryError) return <EmptyLine>프로젝트 정보를 불러오지 못했습니다.</EmptyLine>;
  if (loading) return <ChartSkeleton variant="pie" className="h-[200px]" />;
  if (projects.length === 0) return <EmptyLine>이 기간에 기록된 프로젝트가 없습니다.</EmptyLine>;

  const { slices, others, total } = buildProjectSlices(projects, registry);
  const data: DonutDatum[] = slices.map((s) => ({ key: s.key, name: s.label, value: s.sessionCount, fill: s.color }));
  if (others) {
    data.push({ key: OTHERS_LABEL, name: `${OTHERS_LABEL} ${others.projects.length}개`, value: others.sessionCount, fill: others.color });
  }

  // Ring and legend side by side: stacked, the ring floated alone in a half-page
  // column and pushed the legend below the fold of the figures area.
  return (
    <div className="flex items-start gap-5">
      <div className="size-[128px] shrink-0">
        <ResponsiveContainer width="100%" height="100%">
          <PieChart>
            <Pie
              data={data}
              cx="50%"
              cy="50%"
              innerRadius="58%"
              outerRadius="88%"
              // One project is one whole ring; a padding gap there would suggest a
              // missing slice.
              paddingAngle={data.length > 1 ? 2 : 0}
              startAngle={90}
              endAngle={-270}
              dataKey="value"
              nameKey="name"
              isAnimationActive={false}
            />
            <Tooltip content={<DonutTooltip />} wrapperStyle={{ zIndex: 50 }} isAnimationActive={false} />
          </PieChart>
        </ResponsiveContainer>
      </div>
      <div className="min-w-0 flex-1">
        <p className="px-2 text-[11.5px] text-ink-3">세션 {numberFormat.format(total)}개 기준</p>
        <ul className="mt-1 text-[12.5px]">
          {slices.map((slice) => (
            <li key={slice.key}>
              <ProjectRow row={slice} color={slice.color} />
            </li>
          ))}
          {others && <OthersRow others={others} />}
        </ul>
      </div>
    </div>
  );
};

const ProjectShareDonut = (props: ProjectShareDonutProps) => (
  <section>
    <header className="mb-2 flex items-baseline gap-2">
      <h3 className="text-[13px] font-semibold text-ink-2">Projects</h3>
      <span className="text-[11.5px] text-ink-3">세션 수</span>
    </header>
    <ProjectShareBody {...props} />
  </section>
);

export { buildProjectSlices, ProjectShareDonut };
export type { ProjectOthers, ProjectShareDonutProps, ProjectShareRow, ProjectSlice, ProjectSlices };
