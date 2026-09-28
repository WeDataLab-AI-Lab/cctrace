import type { CSSProperties } from 'react';

interface ToolRateBarProps {
  rate: number;
}

const ToolRateBar = ({ rate }: ToolRateBarProps) => {
  const color = rate >= 80 ? 'var(--success)' : rate >= 50 ? 'var(--warning)' : 'var(--danger)';

  return (
    <div className="flex items-center gap-2">
      <div className="flex-1 bg-border rounded-full h-1.5">
        <div
          className="h-1.5 rounded-full w-[var(--bar-pct)] [background-color:var(--bar-color)]"
          // CSS variables drive runtime bar width/color (Tailwind cannot express dynamic values)
          style={{ '--bar-pct': `${rate}%`, '--bar-color': color } as CSSProperties} // eslint-disable-line no-restricted-syntax
        />
      </div>
      <span className="text-xs text-ink-2 w-10 text-right">{rate.toFixed(0)}%</span>
    </div>
  );
};

export { ToolRateBar };
