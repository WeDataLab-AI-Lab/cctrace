'use client';

import { Palette } from 'lucide-react';
import { cn } from '@/lib/utils';
import {
  useAppearance,
  type Accent,
  type AgentTone,
} from './appearance-context';

interface SegOption<T> {
  value: T;
  label: string;
  dot?: string;
}

interface SegmentedProps<T extends string> {
  value: T;
  onChange: (v: T) => void;
  options: SegOption<T>[];
}

const Segmented = <T extends string>({ value, onChange, options }: SegmentedProps<T>) => (
  <div className="flex rounded-lg overflow-hidden border border-border">
    {options.map((opt) => {
      const active = value === opt.value;
      return (
        <button
          key={opt.value}
          type="button"
          aria-pressed={active}
          onClick={() => onChange(opt.value)}
          className={cn(
            'inline-flex items-center gap-1.5 px-3 py-1.5 text-[12px] font-medium transition-colors',
            active ? 'bg-brand text-brand-ink' : 'bg-surface text-ink-2 hover:bg-surface-sunk',
          )}
        >
          {opt.dot && (
            <span
              aria-hidden="true"
              className={cn(
                'h-2.5 w-2.5 rounded-full',
                opt.dot,
                active && 'ring-2 ring-surface',
              )}
            />
          )}
          {opt.label}
        </button>
      );
    })}
  </div>
);

const Row = ({ label, children }: { label: string; children: React.ReactNode }) => (
  <div className="flex items-center justify-between gap-4">
    <span className="text-[13px] text-ink-2">{label}</span>
    {children}
  </div>
);

const ACCENT: SegOption<Accent>[] = [
  { value: 'indigo', label: 'Indigo', dot: 'bg-accent-indigo' },
  { value: 'teal', label: 'Teal', dot: 'bg-accent-teal' },
  { value: 'mono', label: 'Mono', dot: 'bg-accent-mono' },
];

const TONE: SegOption<AgentTone>[] = [
  { value: 'vivid', label: 'Vivid' },
  { value: 'muted', label: 'Muted' },
];

const AppearanceControls = () => {
  const {
    accent, agentTone, showLogoMark,
    setAccent, setAgentTone, setShowLogoMark,
  } = useAppearance();

  return (
    <div className="bg-surface rounded-lg border border-border p-8 mb-5">
      <div className="flex items-center gap-2.5 mb-1">
        <Palette size={18} className="text-brand" />
        <h2 className="text-[16px] font-semibold text-ink">Appearance</h2>
      </div>
      <p className="text-[12px] text-ink-3 mb-6 ml-[30px]">
        Tune accent and agent marker tone — saved to this browser.
      </p>

      <div className="space-y-4">
        <Row label="Accent"><Segmented value={accent} onChange={setAccent} options={ACCENT} /></Row>
        <Row label="Agent tone"><Segmented value={agentTone} onChange={setAgentTone} options={TONE} /></Row>
        <Row label="Logo mark">
          <button
            type="button"
            role="switch"
            aria-label="Logo mark"
            aria-checked={showLogoMark}
            onClick={() => setShowLogoMark(!showLogoMark)}
            className={cn(
              'relative h-6 w-11 shrink-0 rounded-full border border-border transition-colors',
              showLogoMark ? 'bg-brand' : 'bg-surface-sunk',
            )}
          >
            <span
              className={cn(
                'absolute left-px top-px h-5 w-5 rounded-full bg-surface shadow-[var(--sh-sm)] transition-transform',
                showLogoMark ? 'translate-x-5' : 'translate-x-0',
              )}
            />
          </button>
        </Row>
      </div>
    </div>
  );
};

export { AppearanceControls };
