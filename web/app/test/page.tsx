'use client';

/**
 * /test — DESIGN.md 라이브 쇼케이스.
 * cctrace 디자인 시스템(벤더 중립 · 쿨 화이트 · 데이터 우선)의 토큰·타이포·
 * 컴포넌트를 한 페이지에 정리한 레퍼런스. 상단 Appearance 컨트롤로 accent/
 * agent-tone tweak이 페이지 전체에 라이브 반영된다(Providers 전역).
 * 모든 색은 토큰 유틸로만 참조(인라인 hex 금지). 동적 모델 음영만 colors.ts의
 * 단일 소스(modelColor)를 CSS 변수 채널로 주입.
 */

import { type CSSProperties, useEffect } from 'react';
import { useRouter } from 'next/navigation';
import {
  Palette, Type, LayoutGrid, Layers, Shapes, Component, ListChecks,
  Search, RefreshCw, Inbox, Check, ArrowUpRight, ArrowDownRight,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { modelColor } from '@/lib/colors';
import { CctraceIcon } from '@/components/icons/cctrace-icon';
import { AppearanceControls } from '@/components/common/appearance-controls';

/* ============================================================
   building blocks
   ============================================================ */

const Section = ({
  id, n, icon, title, intro, children,
}: {
  id: string;
  n: string;
  icon: React.ReactNode;
  title: string;
  intro?: string;
  children: React.ReactNode;
}) => (
  <section id={id} className="scroll-mt-8">
    <div className="mb-1 flex items-center gap-2.5">
      <span className="text-brand">{icon}</span>
      <span className="font-mono text-[12px] text-ink-4">{n}</span>
      <h2 className="text-[19px] font-semibold text-ink">{title}</h2>
    </div>
    {intro && (
      <p className="mb-5 ml-[34px] max-w-[72ch] text-[13.5px] leading-relaxed text-ink-2">{intro}</p>
    )}
    {!intro && <div className="mb-5" />}
    {children}
  </section>
);

const SubLabel = ({ children }: { children: React.ReactNode }) => (
  <h3 className="mb-3 text-[11px] font-semibold uppercase tracking-[0.05em] text-ink-3">{children}</h3>
);

const Panel = ({ className, children }: { className?: string; children: React.ReactNode }) => (
  <div className={cn('rounded-[var(--r-lg)] border border-border bg-surface p-[var(--pad-card)]', className)}>
    {children}
  </div>
);

/** 정적 토큰 스와치 — 색 블록 + 이름 + 토큰 + 값(텍스트로만 표기). */
const Swatch = ({
  bg, name, token, value, ring,
}: {
  bg: string;
  name: string;
  token: string;
  value: string;
  ring?: boolean;
}) => (
  <div className="flex flex-col gap-2">
    <span className={cn('h-16 rounded-[var(--r-md)]', ring && 'border border-border', bg)} />
    <span className="text-[12.5px] font-medium text-ink">{name}</span>
    <span className="-mt-1.5 font-mono text-[11px] text-ink-3">{token}</span>
    <span className="-mt-1 font-mono text-[11px] text-ink-4">{value}</span>
  </div>
);

/** 동적 모델 음영 칩 — colors.ts modelColor를 CSS 변수로 주입(단일 소스). */
const ShadeChip = ({ model, label }: { model: string; label: string }) => (
  <div className="flex flex-col items-center gap-1.5">
    <span
      className="h-14 w-14 rounded-[var(--r-md)] border border-border-subtle bg-[var(--chip)]"
      style={{ '--chip': modelColor(model) } as CSSProperties} // eslint-disable-line no-restricted-syntax
    />
    <span className="font-mono text-[11px] text-ink-2">{label}</span>
  </div>
);

/* ============================================================
   data
   ============================================================ */

const SURFACES = [
  { bg: 'bg-canvas', name: 'Canvas', token: '--canvas', value: '#F6F7F9', ring: true },
  { bg: 'bg-surface', name: 'Surface', token: '--surface', value: '#FFFFFF', ring: true },
  { bg: 'bg-surface-2', name: 'Surface-2', token: '--surface-2', value: '#FBFCFD', ring: true },
  { bg: 'bg-surface-sunk', name: 'Surface-sunk', token: '--surface-sunk', value: '#F1F3F6', ring: true },
];

const BORDERS = [
  { bg: 'bg-[var(--border)]', name: 'Border', token: '--border', value: '#E6E8EC', ring: true },
  { bg: 'bg-border-subtle', name: 'Border-subtle', token: '--border-subtle', value: '#EEF0F3', ring: true },
  { bg: 'bg-border-strong', name: 'Border-strong', token: '--border-strong', value: '#D6DAE0', ring: true },
];

const INKS = [
  { bg: 'bg-ink', name: 'Ink', token: '--ink', value: '#0F1419' },
  { bg: 'bg-ink-2', name: 'Ink-2', token: '--ink-2', value: '#5B6370' },
  { bg: 'bg-ink-3', name: 'Ink-3', token: '--ink-3', value: '#8A909C' },
  { bg: 'bg-ink-4', name: 'Ink-4', token: '--ink-4', value: '#AEB3BD' },
];

const ACCENTS = [
  { bg: 'bg-brand', name: 'Accent', token: '--accent', value: '#5B5BD6' },
  { bg: 'bg-brand-hover', name: 'Accent-hover', token: '--accent-hover', value: '#4B4BC4' },
  { bg: 'bg-brand-soft', name: 'Accent-soft', token: '--accent-soft', value: '#ECECFB', ring: true },
];

const AGENTS = [
  { key: 'claude', label: 'Claude Code', token: '--agent-claude', value: 'oklch(0.61 0.16 47)', id: '테라코타 오렌지', base: 'bg-agent-claude', soft: 'bg-agent-claude-soft', text: 'text-agent-claude' },
  { key: 'codex', label: 'OpenAI Codex', token: '--agent-codex', value: 'oklch(0.55 0.165 300)', id: '바이올렛', base: 'bg-agent-codex', soft: 'bg-agent-codex-soft', text: 'text-agent-codex' },
  { key: 'compat', label: 'Compatible', token: '--agent-compat', value: 'oklch(0.62 0.115 182)', id: '틸', base: 'bg-agent-compat', soft: 'bg-agent-compat-soft', text: 'text-agent-compat' },
];

const SHADE_RAMPS = [
  { label: 'Claude', chips: [{ model: 'claude-fable-5', label: 'fable 5' }, { model: 'claude-opus-4-8', label: 'opus 4.8' }, { model: 'claude-sonnet-5', label: 'sonnet 5' }, { model: 'claude-haiku-4-5', label: 'haiku 4.5' }] },
  { label: 'Codex', chips: [{ model: 'gpt-6-astra', label: '6 astra' }, { model: 'gpt-5.6-sol', label: '5.6 sol' }, { model: 'gpt-5.6-terra', label: '5.6 terra' }, { model: 'gpt-5.6-luna', label: '5.6 luna' }, { model: 'gpt-5.5', label: 'gpt-5.5' }, { model: 'gpt-5.4', label: 'gpt-5.4' }, { model: 'gpt-5.3-codex', label: 'gpt-5.3' }, { model: 'gpt-5.4-mini', label: 'mini' }] },
  { label: 'Compatible', chips: [{ model: 'qwen-3-coder', label: 'qwen-3-coder' }, { model: 'glm-4.6', label: 'glm-4.6' }, { model: 'kimi-k2', label: 'kimi-k2' }, { model: 'deepseek-v3', label: 'deepseek-v3' }, { model: 'gemini-2.5-pro', label: 'gemini-2.5' }, { model: 'llama-3.3-70b', label: 'llama-3.3' }, { model: 'mistral-large', label: 'mistral' }] },
];

const CATS = [
  { bg: 'bg-cat-1', name: '인디고', token: '--cat-1' },
  { bg: 'bg-cat-2', name: '틸', token: '--cat-2' },
  { bg: 'bg-cat-3', name: '앰버', token: '--cat-3' },
  { bg: 'bg-cat-4', name: '로즈', token: '--cat-4' },
  { bg: 'bg-cat-5', name: '그린', token: '--cat-5' },
  { bg: 'bg-cat-6', name: '바이올렛', token: '--cat-6' },
  { bg: 'bg-cat-7', name: '스카이', token: '--cat-7' },
  { bg: 'bg-cat-8', name: '라임', token: '--cat-8' },
];

const AGGREGATES = [
  { model: 'Others', label: 'Others', note: '--agent-compat aggregate bucket' },
];

const SEMANTICS = [
  { base: 'bg-success', soft: 'bg-success-soft', strong: 'text-success-strong', name: 'Success', token: '--success', value: 'oklch(0.62 0.13 152)', use: '성공 · available' },
  { base: 'bg-danger', soft: 'bg-danger-soft', strong: 'text-danger-strong', name: 'Danger', token: '--danger', value: 'oklch(0.585 0.19 25)', use: '실패 · 에러 · 검증' },
  { base: 'bg-warning', soft: 'bg-warning-soft', strong: 'text-warning-strong', name: 'Warning', token: '--warning', value: 'oklch(0.74 0.14 75)', use: '경고' },
];

const TYPE_ROWS = [
  { sample: '페이지 타이틀', cls: 'text-[24px] font-semibold text-ink', meta: '22–26px · 600 · 0em' },
  { sample: '섹션 / 카드 헤딩', cls: 'text-[18px] font-semibold text-ink', meta: '17–19px · 600' },
  { sample: '34,182', cls: 'text-[30px] font-semibold tabular-nums text-ink', meta: 'KPI / 메트릭 · 28–34px · 600 · tabular-nums' },
  { sample: '카드 타이틀', cls: 'text-[15px] font-medium text-ink', meta: '15–16px · 500–600' },
  { sample: '본문 텍스트 — 깨끗한 휴머니스트 산세리프.', cls: 'text-[13.5px] text-ink-2', meta: '13.5px · 400 · lh 1.45' },
  { sample: '라벨 / 캡션', cls: 'text-[12px] font-medium text-ink-3', meta: '11.5–12.5px · 500 · ink-3' },
  { sample: 'UPPERCASE 태그', cls: 'text-[11px] font-semibold uppercase tracking-[0.05em] text-ink-3', meta: '11px · 600 · ls 0.04–0.06em' },
  { sample: 'claude-opus-4-8 · 0xA1B2', cls: 'font-mono text-[13px] text-ink-2', meta: '코드 / ID · JetBrains Mono · 12–13.5px' },
];

const SHADOWS = [
  { name: 'Flat', sh: '', token: '없음', use: '본문 · 사이드바 · 헤더', flat: true },
  { name: 'Hairline', sh: '', token: '1px --border', use: '인풋 · 카드 외곽 · 셀', hair: true },
  { name: 'Card', sh: 'shadow-[var(--sh-sm)]', token: '--sh-sm', use: 'SummaryCard · 패널' },
  { name: 'Raised', sh: 'shadow-[var(--sh-md)]', token: '--sh-md', use: '차트 카드 · 강조' },
  { name: 'Popover', sh: 'shadow-[var(--sh-pop)]', token: '--sh-pop', use: '드롭다운 · 메뉴 · 툴팁' },
];

const RADII = [
  { name: 'r-xs', token: '--r-xs', value: '5px', r: 'rounded-[var(--r-xs)]' },
  { name: 'r-sm', token: '--r-sm', value: '7px', r: 'rounded-[var(--r-sm)]' },
  { name: 'r-md', token: '--r-md', value: '10px', r: 'rounded-[var(--r-md)]' },
  { name: 'r-lg', token: '--r-lg', value: '14px', r: 'rounded-[var(--r-lg)]' },
  { name: 'r-pill', token: '--r-pill', value: '999px', r: 'rounded-[var(--r-pill)]' },
];

const SPACING = [
  { name: 'pad-card', token: '--pad-card', value: '20px', bar: 'w-[20px]' },
  { name: 'sidebar-w', token: '--sidebar-w', value: '220px', bar: 'w-[220px] max-w-full' },
];

const CAT_BARS = [
  'flex-[2]', 'flex-[3]', 'flex-[2]', 'flex-[4]', 'flex-[3]', 'flex-[2]', 'flex-[3]', 'flex-[2]',
];

const DOS = [
  '모든 화면을 쿨 화이트 캔버스(--canvas)에 앵커.',
  '크롬은 무채색으로 물러나게 — 색은 데이터 · 의미 · 에이전트에만.',
  'Claude · Codex · Compatible을 항상 동등하게 — 채도 대등한 고유 색.',
  '모델은 같은 hue 안에서 좋을수록 진하게.',
  '모든 숫자에 tabular-nums. ID · 모델 · 코드는 모노.',
  '액센트(--accent)는 인색하게 — CTA · 활성 · 포커스 · 로고에만.',
  '차트는 색약 안전 --cat-* 팔레트.',
  '색은 CSS 변수로만 참조 — 인라인 hex 금지.',
];

const DONTS = [
  '따뜻한 크림 · 아이보리 · 테라코타 (Anthropic 차용).',
  'OpenAI 시그니처 블랙 · 그린을 크롬에.',
  '"non-Claude = 회색" 같은 에이전트 편향.',
  '헤드라인 700 bold (관제판은 600까지).',
  '어스톤 muted 차트 팔레트.',
  '본문에 슬랩세리프 / 에디토리얼 보이스.',
  '4번째 크롬 톤 도입 (쿨 뉴트럴 4단계면 충분).',
  '장식적 무한 애니메이션 · Arial 폴백 잔재.',
];

const TOC = [
  { id: 'colors', label: 'Colors' },
  { id: 'typography', label: 'Typography' },
  { id: 'layout', label: 'Layout' },
  { id: 'elevation', label: 'Elevation' },
  { id: 'shapes', label: 'Shapes' },
  { id: 'components', label: 'Components' },
  { id: 'guidelines', label: "Do's & Don'ts" },
];

/* ============================================================
   page
   ============================================================ */

export default function DesignSystemPage() {
  const router = useRouter();

  useEffect(() => {
    if (process.env.NODE_ENV === 'production') {
      router.replace('/');
    }
  }, [router]);

  if (process.env.NODE_ENV === 'production') {
    return null;
  }

  return (
    <main className="page-fade min-h-screen bg-canvas">
      <div className="mx-auto max-w-[1120px] px-8 py-12">

        {/* ---- hero ---- */}
        <header className="mb-12">
          <div className="mb-5 flex items-center gap-3">
            <CctraceIcon size={40} />
            <div className="flex flex-col">
              <span className="font-mono text-[13px] font-medium text-ink">cctrace</span>
              <span className="font-mono text-[11px] text-ink-3">DESIGN.md · v0602</span>
            </div>
          </div>
          <h1 className="mb-3 text-[26px] font-semibold text-ink">디자인 시스템</h1>
          <p className="max-w-[74ch] text-[14px] leading-relaxed text-ink-2">
            AI 코딩 에이전트 관제(observability) 대시보드 —{' '}
            <span className="font-medium text-ink">벤더 중립 · 쿨 화이트 · 데이터 우선</span>.
            은유는 단 하나, <span className="font-medium text-ink">&ldquo;AI 에이전트들의 계측 패널&rdquo;</span>.
            크롬은 무채색으로 물러나고 색은 데이터 · 의미 · 에이전트에만 쓰입니다. 어떤 벤더의 색도 빌리지 않은 cctrace 자신의 톤.
          </p>
          <div className="mt-4 flex flex-wrap gap-2">
            {['Next.js 16', 'React 19', 'Tailwind v4', 'Recharts 3', 'lucide-react', '라이트 전용'].map((t) => (
              <span key={t} className="rounded-[var(--r-pill)] border border-border bg-surface px-2.5 py-1 font-mono text-[11px] text-ink-2">
                {t}
              </span>
            ))}
          </div>

          {/* table of contents */}
          <nav className="mt-7 flex flex-wrap gap-2">
            {TOC.map((t, i) => (
              <a
                key={t.id}
                href={`#${t.id}`}
                className="inline-flex items-center gap-1.5 rounded-[var(--r-md)] border border-border bg-surface px-3 py-1.5 text-[12.5px] text-ink-2 transition-colors hover:border-border-strong hover:bg-surface-2 hover:text-ink"
              >
                <span className="font-mono text-[11px] text-ink-4">{String(i + 1).padStart(2, '0')}</span>
                {t.label}
              </a>
            ))}
          </nav>
        </header>

        {/* ---- live tweaks ---- */}
        <p className="mb-3 text-[12px] text-ink-3">
          아래 컨트롤을 바꾸면 accent · agent-tone · 로고가 이 페이지 전체에 라이브로 반영됩니다.
        </p>
        <AppearanceControls />

        <div className="space-y-14">

          {/* ============ COLORS ============ */}
          <Section
            id="colors"
            n="01"
            icon={<Palette size={18} strokeWidth={1.75} />}
            title="Colors"
            intro="원칙: 크롬은 무채색, 색은 데이터·의미에만. 모든 색은 CSS 변수로만 참조하고 인라인 hex는 금지."
          >
            <div className="space-y-7">
              <div>
                <SubLabel>베이스 — 쿨 뉴트럴 서피스</SubLabel>
                <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
                  {SURFACES.map((s) => <Swatch key={s.token} {...s} />)}
                </div>
              </div>

              <div>
                <SubLabel>경계선 — 헤어라인</SubLabel>
                <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4">
                  {BORDERS.map((s) => <Swatch key={s.token} {...s} />)}
                </div>
              </div>

              <div>
                <SubLabel>잉크 위계 — 쿨 그레이</SubLabel>
                <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
                  {INKS.map((s) => <Swatch key={s.token} {...s} />)}
                </div>
              </div>

              <div>
                <SubLabel>브랜드 액센트 — 단일 시그널</SubLabel>
                <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4">
                  {ACCENTS.map((s) => <Swatch key={s.token} {...s} />)}
                </div>
                <div className="mt-4 flex flex-wrap items-center gap-3 rounded-[var(--r-md)] bg-surface-sunk px-4 py-3">
                  <span className="text-[12px] text-ink-3">변형(tweak) <code className="font-mono text-ink-2">data-accent</code></span>
                  {[['indigo', 'bg-accent-indigo'], ['teal', 'bg-accent-teal'], ['mono', 'bg-accent-mono']].map(([name, dot]) => (
                    <span key={name} className="inline-flex items-center gap-1.5 text-[12px] text-ink-2">
                      <span className={cn('h-3 w-3 rounded-full', dot)} />{name}
                    </span>
                  ))}
                </div>
              </div>

              {/* agent palette */}
              <div>
                <SubLabel>에이전트 팔레트 ⭐ — 에이전트 동등</SubLabel>
                <p className="-mt-1 mb-4 max-w-[72ch] text-[12.5px] leading-relaxed text-ink-3">
                  세 에이전트는 동급 1등 시민. 한쪽만 컬러·다른 쪽 회색은 금지. OKLCH로 채도를 대등하게 유지.
                </p>
                <div className="grid gap-4 lg:grid-cols-3">
                  {AGENTS.map((a) => (
                    <Panel key={a.key} className="flex flex-col gap-3">
                      <div className="flex items-center gap-2.5">
                        <span className={cn('h-9 w-9 rounded-[var(--r-md)]', a.base)} />
                        <span className={cn('h-9 w-9 rounded-[var(--r-md)] border border-border', a.soft)} />
                        <div className="flex flex-col">
                          <span className="text-[13px] font-medium text-ink">{a.label}</span>
                          <span className="font-mono text-[11px] text-ink-3">{a.token}</span>
                        </div>
                      </div>
                      <span className="font-mono text-[11px] text-ink-4">{a.value}</span>
                      <span className={cn('inline-flex w-fit items-center gap-1.5 rounded-[var(--r-pill)] px-2.5 py-1 text-[11.5px] font-medium', a.soft, a.text)}>
                        <span className={cn('h-2 w-2 rounded-full', a.base)} />{a.id}
                      </span>
                    </Panel>
                  ))}
                </div>

                {/* model shade ramps */}
                <div className="mt-4 rounded-[var(--r-lg)] border border-border bg-surface p-[var(--pad-card)]">
                  <p className="mb-4 text-[12.5px] text-ink-2">
                    모델별 음영 — 같은 hue 안에서 <span className="font-medium text-ink">좋은 모델일수록 진하게</span>
                    <span className="text-ink-4"> (단일 소스: <code className="font-mono">lib/colors.ts</code> · modelColor)</span>
                  </p>
                  <div className="flex flex-wrap gap-x-9 gap-y-5">
                    {SHADE_RAMPS.map((r) => (
                      <div key={r.label} className="flex flex-col gap-2">
                        <span className="font-mono text-[11px] text-ink-3">{r.label} · 진함 → 옅음</span>
                        <div className="flex items-end gap-3">
                          {r.chips.map((c) => <ShadeChip key={c.model} {...c} />)}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>

                {/* fable — flagship hybrid */}
                <div className="mt-4 rounded-[var(--r-lg)] border border-border bg-surface p-[var(--pad-card)]">
                  <p className="mb-4 text-[12.5px] text-ink-2">
                    Fable ✦ — <span className="font-medium text-ink">플래그십 하이브리드</span>
                    <span className="text-ink-4"> · 차트/툴팁=솔리드 앵커, 배지/도트=그라데이션 (SVG fill에 그라데이션 금지)</span>
                  </p>
                  <div className="flex flex-wrap items-center gap-7">
                    <div className="flex flex-col gap-1.5">
                      <span className="h-14 w-32 rounded-[var(--r-md)] bg-model-fable" />
                      <span className="font-mono text-[11px] text-ink-3">--model-fable (flagship)</span>
                      <span className="-mt-1 font-mono text-[10px] text-ink-4">엠버 → 딥 로즈 → 마젠타 · 135deg</span>
                    </div>
                    <div className="flex flex-col gap-1.5">
                      <span className="h-14 w-14 rounded-[var(--r-md)] bg-model-fable" />
                      <span className="font-mono text-[11px] text-ink-3">--model-fable</span>
                      <span className="-mt-1 font-mono text-[10px] text-ink-4">oklch(0.53 0.19 38)</span>
                    </div>
                    <span className="inline-flex items-center gap-1.5 rounded-[var(--r-pill)] bg-model-fable-soft px-2.5 py-1 text-[11.5px] font-medium text-model-fable">
                      <span className="h-2 w-2 rounded-full bg-model-fable" />claude-fable-5
                    </span>
                  </div>
                </div>

                {/* astra — Codex flagship */}
                <div className="mt-4 rounded-[var(--r-lg)] border border-border bg-surface p-[var(--pad-card)]">
                  <p className="mb-4 text-[12.5px] text-ink-2">
                    Astra — <span className="font-medium text-ink">Codex 플래그십</span>
                    <span className="text-ink-4"> · 차트/툴팁/배지/도트 모두 솔리드 앵커, 카프리 램프(hue 232)의 최심부</span>
                  </p>
                  <div className="flex flex-wrap items-center gap-7">
                    <div className="flex flex-col gap-1.5">
                      <span className="h-14 w-32 rounded-[var(--r-md)] bg-model-astra" />
                      <span className="font-mono text-[11px] text-ink-3">--model-astra (flagship)</span>
                      <span className="-mt-1 font-mono text-[10px] text-ink-4">oklch(0.46 0.130 232)</span>
                    </div>
                    <div className="flex flex-col gap-1.5">
                      <span className="h-14 w-14 rounded-[var(--r-md)] bg-model-astra" />
                      <span className="font-mono text-[11px] text-ink-3">--model-astra</span>
                      <span className="-mt-1 font-mono text-[10px] text-ink-4">oklch(0.46 0.130 232)</span>
                    </div>
                    <span className="inline-flex items-center gap-1.5 rounded-[var(--r-pill)] bg-model-astra-soft px-2.5 py-1 text-[11.5px] font-medium text-model-astra">
                      <span className="h-2 w-2 rounded-full bg-model-astra" />gpt-6-astra
                    </span>
                  </div>
                </div>
              </div>

              {/* categorical */}
              <div>
                <SubLabel>데이터 시각화 — 색약 안전 8색</SubLabel>
                <div className="grid grid-cols-4 gap-3 sm:grid-cols-8">
                  {CATS.map((c) => (
                    <div key={c.token} className="flex flex-col items-center gap-1.5">
                      <span className={cn('h-12 w-full rounded-[var(--r-sm)]', c.bg)} />
                      <span className="text-[11px] text-ink-2">{c.name}</span>
                      <span className="-mt-1 font-mono text-[10px] text-ink-4">{c.token}</span>
                    </div>
                  ))}
                </div>
                {/* mini stacked bar */}
                <div className="mt-4 flex h-7 overflow-hidden rounded-[var(--r-sm)]">
                  {CATS.map((c, i) => (
                    <span key={c.token} className={cn('h-full', c.bg, CAT_BARS[i])} />
                  ))}
                </div>
                <div className="mt-4 flex flex-wrap gap-3">
                  {AGGREGATES.map((item) => (
                    <div key={item.model} className="flex items-center gap-3 rounded-[var(--r-md)] border border-border bg-surface px-3 py-2">
                      <ShadeChip model={item.model} label={item.label} />
                      <span className="font-mono text-[11px] text-ink-3">{item.note}</span>
                    </div>
                  ))}
                </div>
              </div>

              {/* semantic */}
              <div>
                <SubLabel>의미 색 — Semantic</SubLabel>
                <div className="grid gap-4 sm:grid-cols-3">
                  {SEMANTICS.map((s) => (
                    <Panel key={s.token} className="flex flex-col gap-3">
                      <div className="flex items-center gap-2.5">
                        <span className={cn('h-8 w-8 rounded-[var(--r-md)]', s.base)} />
                        <span className={cn('h-8 w-8 rounded-[var(--r-md)] border border-border', s.soft)} />
                        <div className="flex flex-col">
                          <span className="text-[13px] font-medium text-ink">{s.name}</span>
                          <span className="font-mono text-[11px] text-ink-3">{s.token}</span>
                        </div>
                      </div>
                      <span className={cn('inline-flex w-fit items-center rounded-[var(--r-pill)] px-2.5 py-1 text-[11.5px] font-medium', s.soft, s.strong)}>
                        {s.use}
                      </span>
                      <span className="font-mono text-[11px] text-ink-4">{s.value}</span>
                    </Panel>
                  ))}
                </div>
              </div>
            </div>
          </Section>

          {/* ============ TYPOGRAPHY ============ */}
          <Section
            id="typography"
            n="02"
            icon={<Type size={18} strokeWidth={1.75} />}
            title="Typography"
            intro="Inter(휴머니스트 산세리프)를 본문·UI에, JetBrains Mono(진짜 모노)를 ID·모델·코드·메트릭에. 모든 숫자는 tabular-nums."
          >
            <div className="grid gap-4 lg:grid-cols-2">
              <Panel>
                <span className="font-mono text-[11px] text-ink-3">--font-sans</span>
                <p className="mt-1 text-[28px] font-semibold text-ink">Inter</p>
                <p className="text-[13px] text-ink-2">본문 · 헤드라인 · UI 라벨 (weight 400–600)</p>
                <p className="mt-2 text-[15px] text-ink">다람쥐 헌 쳇바퀴에 타고파 0123456789</p>
              </Panel>
              <Panel>
                <span className="font-mono text-[11px] text-ink-3">--font-mono</span>
                <p className="mt-1 font-mono text-[28px] font-semibold text-ink">JetBrains Mono</p>
                <p className="font-mono text-[13px] text-ink-2">ID · 모델 · 코드 · 로그 · 수치</p>
                <p className="mt-2 font-mono text-[15px] tabular-nums text-ink">claude-opus-4-8 · 1,234,567</p>
              </Panel>
            </div>

            <div className="mt-4 overflow-hidden rounded-[var(--r-lg)] border border-border">
              {TYPE_ROWS.map((r, i) => (
                <div
                  key={r.meta}
                  className={cn(
                    'flex flex-col gap-1 px-5 py-3.5 sm:flex-row sm:items-baseline sm:justify-between',
                    i % 2 === 1 ? 'bg-surface-2' : 'bg-surface',
                    i > 0 && 'border-t border-border-subtle',
                  )}
                >
                  <span className={r.cls}>{r.sample}</span>
                  <span className="font-mono text-[11px] text-ink-4">{r.meta}</span>
                </div>
              ))}
            </div>

            <Panel className="mt-4 bg-surface-2">
              <ul className="space-y-1.5 text-[12.5px] text-ink-2">
                <li>· 헤드라인은 굵기 <span className="font-medium text-ink">600까지만</span>. 700 bold는 관제판에서 시끄러움.</li>
                <li>· 강조는 굵기보다 <span className="font-medium text-ink">색 · 위치 · 크기</span>로.</li>
                <li>· ID · 모델명 · 해시 · 타임스탬프 · 토큰 수는 반드시 <span className="font-medium text-ink">모노 + tabular-nums</span>.</li>
              </ul>
            </Panel>
          </Section>

          {/* ============ LAYOUT ============ */}
          <Section
            id="layout"
            n="03"
            icon={<LayoutGrid size={18} strokeWidth={1.75} />}
            title="Layout"
            intro="기본 단위 4px. 좌측 고정 사이드바(220px) + 헤더 + 스크롤 본문. 여백은 고정 토큰으로 일관되게 사용."
          >
            <div>
              <Panel>
                <SubLabel>스페이싱 토큰</SubLabel>
                <div className="space-y-3">
                  {SPACING.map((s) => (
                    <div key={s.token} className="flex items-center gap-3">
                      <span className={cn('h-3.5 rounded-[var(--r-xs)] bg-brand-soft', s.bar)} />
                      <span className="font-mono text-[11px] text-ink-3">{s.token}</span>
                      <span className="font-mono text-[11px] tabular-nums text-ink-4">{s.value}</span>
                    </div>
                  ))}
                </div>
              </Panel>
            </div>

            {/* app shell sketch */}
            <Panel className="mt-4">
              <SubLabel>앱 셸</SubLabel>
              <div className="flex h-44 overflow-hidden rounded-[var(--r-md)] border border-border">
                <div className="flex w-[110px] shrink-0 flex-col gap-2 border-r border-border bg-surface p-3">
                  <div className="flex items-center gap-1.5"><CctraceIcon size={16} /><span className="h-2 w-12 rounded-full bg-surface-sunk" /></div>
                  <div className="mt-2 h-1.5 w-8 rounded-full bg-brand" />
                  <div className="h-2.5 rounded-[var(--r-sm)] bg-brand-soft" />
                  {Array.from({ length: 4 }).map((_, i) => <div key={i} className="h-2.5 rounded-[var(--r-sm)] bg-surface-sunk" />)}
                  <span className="mt-auto font-mono text-[9px] text-ink-4">220px</span>
                </div>
                <div className="flex flex-1 flex-col">
                  <div className="flex h-9 items-center justify-between border-b border-border bg-surface px-3">
                    <span className="h-2.5 w-20 rounded-full bg-surface-sunk" />
                    <span className="h-4 w-12 rounded-[var(--r-pill)] bg-brand" />
                  </div>
                  <div className="grid flex-1 grid-cols-4 gap-2 bg-canvas p-3">
                    {Array.from({ length: 4 }).map((_, i) => (
                      <div key={i} className="rounded-[var(--r-sm)] border border-border bg-surface shadow-[var(--sh-sm)]" />
                    ))}
                  </div>
                </div>
              </div>
            </Panel>
          </Section>

          {/* ============ ELEVATION ============ */}
          <Section
            id="elevation"
            n="04"
            icon={<Layers size={18} strokeWidth={1.75} />}
            title="Elevation & Depth"
            intro="깊이 철학은 헤어라인 우선, 그림자 절제. 대부분의 단차는 서피스 톤과 1px 경계선으로, 그림자는 떠 있는 요소에만 쿨톤으로 낮게."
          >
            <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-5">
              {SHADOWS.map((s) => (
                <div key={s.name} className="flex flex-col items-center gap-3">
                  <div
                    className={cn(
                      'flex h-20 w-full items-center justify-center rounded-[var(--r-lg)] bg-surface',
                      s.flat && 'bg-canvas',
                      s.hair && 'border border-border',
                      s.sh,
                    )}
                  >
                    <span className="font-mono text-[11px] text-ink-3">{s.name}</span>
                  </div>
                  <div className="flex flex-col items-center text-center">
                    <span className="font-mono text-[11px] text-ink-3">{s.token}</span>
                    <span className="mt-0.5 text-[11px] text-ink-4">{s.use}</span>
                  </div>
                </div>
              ))}
            </div>
          </Section>

          {/* ============ SHAPES ============ */}
          <Section
            id="shapes"
            n="05"
            icon={<Shapes size={18} strokeWidth={1.75} />}
            title="Shapes"
            intro="라디우스 위계 — 작은 요소부터 카드·배지까지. 아이콘은 lucide-react(stroke 1.75), 로고는 에이전트 그라데이션 눈동자."
          >
            <div className="flex flex-wrap gap-6">
              {RADII.map((r) => (
                <div key={r.token} className="flex flex-col items-center gap-2">
                  <span className={cn('h-20 w-20 border border-brand bg-brand-soft', r.r)} />
                  <span className="text-[12px] font-medium text-ink">{r.name}</span>
                  <span className="-mt-1.5 font-mono text-[11px] text-ink-4">{r.value}</span>
                </div>
              ))}
            </div>

            <Panel className="mt-5 flex flex-wrap items-center gap-8">
              <div className="flex flex-col items-center gap-2">
                <CctraceIcon size={56} />
                <span className="font-mono text-[11px] text-ink-3">로고 마크 (무채색)</span>
              </div>
              <div className="flex flex-col items-center gap-2">
                <CctraceIcon size={56} className="opacity-30" />
                <span className="font-mono text-[11px] text-ink-3">사이드바 표시(30%)</span>
              </div>
              <div className="flex items-center gap-4">
                {[Search, RefreshCw, Inbox, Layers, Shapes].map((Icon, i) => (
                  <Icon key={i} size={24} strokeWidth={1.75} className="text-ink-2" />
                ))}
              </div>
            </Panel>
          </Section>

          {/* ============ COMPONENTS ============ */}
          <Section
            id="components"
            n="06"
            icon={<Component size={18} strokeWidth={1.75} />}
            title="Components"
            intro="크롬은 무채색, 데이터는 컬러. 버튼·배지·카드·인풋·테이블·빈 상태 — 토큰만으로 조립."
          >
            <div className="space-y-4">
              {/* buttons */}
              <Panel>
                <SubLabel>Buttons</SubLabel>
                <div className="flex flex-wrap items-center gap-3">
                  <button className="inline-flex h-9 items-center rounded-[var(--r-md)] bg-brand px-4 text-[13px] font-medium text-brand-ink transition-colors hover:bg-brand-hover">Primary</button>
                  <button className="inline-flex h-9 items-center rounded-[var(--r-md)] border border-border bg-surface px-4 text-[13px] font-medium text-ink transition-colors hover:border-border-strong hover:bg-surface-2">Secondary</button>
                  <button className="inline-flex h-9 items-center rounded-[var(--r-md)] px-4 text-[13px] font-medium text-ink transition-colors hover:bg-surface-sunk">Ghost</button>
                  <button className="inline-flex h-9 items-center gap-1.5 rounded-[var(--r-md)] px-3 text-[13px] font-medium text-ink-2 transition-colors hover:bg-surface-sunk"><RefreshCw size={15} strokeWidth={1.75} />새로고침</button>
                  <button disabled className="inline-flex h-9 cursor-not-allowed items-center rounded-[var(--r-md)] bg-brand px-4 text-[13px] font-medium text-brand-ink opacity-50">Disabled</button>
                </div>
              </Panel>

              {/* badges */}
              <Panel>
                <SubLabel>Badges</SubLabel>
                <div className="flex flex-wrap items-center gap-2.5">
                  {AGENTS.map((a) => (
                    <span key={a.key} className={cn('inline-flex items-center gap-1.5 rounded-[var(--r-pill)] px-2.5 py-1 text-[11.5px] font-medium', a.soft, a.text)}>
                      <span className={cn('h-2 w-2 rounded-full', a.base)} />{a.label}
                    </span>
                  ))}
                  <span className="inline-flex items-center gap-1.5 rounded-[var(--r-pill)] bg-model-fable-soft px-2.5 py-1 text-[11.5px] font-medium text-model-fable"><span className="h-2 w-2 rounded-full bg-model-fable" />fable-5 ✦</span>
                  <span className="inline-flex items-center gap-1.5 rounded-[var(--r-pill)] bg-model-astra-soft px-2.5 py-1 text-[11.5px] font-medium text-model-astra"><span className="h-2 w-2 rounded-full bg-model-astra" />gpt-6-astra</span>
                  <span className="inline-flex items-center gap-1.5 rounded-[var(--r-pill)] bg-success-soft px-2.5 py-1 text-[11.5px] font-medium text-success-strong"><span className="h-2 w-2 rounded-full bg-success" />available</span>
                  <span className="inline-flex items-center gap-1.5 rounded-[var(--r-pill)] bg-danger-soft px-2.5 py-1 text-[11.5px] font-medium text-danger-strong"><span className="h-2 w-2 rounded-full bg-danger" />error</span>
                  <span className="inline-flex items-center gap-1.5 rounded-[var(--r-pill)] bg-warning-soft px-2.5 py-1 text-[11.5px] font-medium text-warning-strong"><span className="h-2 w-2 rounded-full bg-warning" />warning</span>
                </div>
              </Panel>

              {/* summary cards (KPI) */}
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
                {[
                  { label: 'TOTAL TOKENS', value: '34.1M', delta: '+12.4%', up: true },
                  { label: 'SESSIONS', value: '1,284', delta: '+5.2%', up: true },
                  { label: 'COST (USD)', value: '$842.50', delta: '−3.1%', up: false },
                  { label: 'TOOL CALLS', value: '52,907', delta: '+8.8%', up: true },
                ].map((k) => (
                  <div key={k.label} className="rounded-[var(--r-lg)] border border-border bg-surface p-[var(--pad-card)] shadow-[var(--sh-sm)]">
                    <span className="text-[11px] font-semibold uppercase tracking-[0.05em] text-ink-3">{k.label}</span>
                    <p className="mt-2 text-[30px] font-semibold tabular-nums leading-none text-ink">{k.value}</p>
                    <span className={cn('mt-2 inline-flex items-center gap-1 text-[12px] font-medium tabular-nums', k.up ? 'text-success-strong' : 'text-danger-strong')}>
                      {k.up ? <ArrowUpRight size={14} /> : <ArrowDownRight size={14} />}{k.delta}
                    </span>
                  </div>
                ))}
              </div>

              {/* input + checkbox/toggle */}
              <div className="grid gap-4 lg:grid-cols-2">
                <Panel>
                  <SubLabel>Inputs</SubLabel>
                  <div className="relative">
                    <Search size={16} strokeWidth={1.75} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-ink-3" />
                    <input
                      type="text"
                      placeholder="Search sessions…"
                      className="h-11 w-full rounded-[var(--r-md)] border border-border bg-surface pl-9 pr-3 text-[13.5px] text-ink outline-none transition-shadow placeholder:text-ink-4 focus:border-brand focus:ring-[3px] focus:ring-brand/30"
                    />
                  </div>
                  <p className="mt-2 text-[11.5px] text-ink-4">포커스 → --accent 테두리 + 3px --accent-ring</p>
                </Panel>
                <Panel>
                  <SubLabel>Checkbox · Toggle</SubLabel>
                  <div className="flex items-center gap-5">
                    <span className="inline-flex items-center gap-2 text-[13px] text-ink">
                      <span className="inline-flex h-[18px] w-[18px] items-center justify-center rounded-[5px] bg-brand text-brand-ink"><Check size={13} strokeWidth={3} /></span>
                      선택됨
                    </span>
                    <span className="inline-flex items-center gap-2 text-[13px] text-ink-2">
                      <span className="h-[18px] w-[18px] rounded-[5px] border border-border-strong bg-surface" />
                      해제
                    </span>
                    <span className="relative inline-block h-6 w-11 rounded-full bg-brand">
                      <span className="absolute top-0.5 right-0.5 h-5 w-5 rounded-full bg-surface shadow-[var(--sh-sm)]" />
                    </span>
                  </div>
                </Panel>
              </div>

              {/* data table */}
              <Panel className="p-0">
                <div className="border-b border-border px-5 py-3"><SubLabel>Data table</SubLabel><span className="sr-only">table</span></div>
                <table className="w-full text-[12.5px]">
                  <thead>
                    <tr className="border-b border-border bg-surface-2 text-left text-[11px] font-semibold uppercase tracking-[0.04em] text-ink-3">
                      <th className="px-5 py-2.5 font-semibold">Session ID</th>
                      <th className="px-5 py-2.5 font-semibold">Model</th>
                      <th className="px-5 py-2.5 text-right font-semibold">Tokens</th>
                      <th className="px-5 py-2.5 text-right font-semibold">Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {[
                      { id: '0xA1B2C3', model: 'opus', agent: AGENTS[0], tok: '1,284,901', ok: true },
                      { id: '0xD4E5F6', model: 'gpt-5.6-sol', agent: AGENTS[1], tok: '842,110', ok: true },
                      { id: '0x778899', model: 'qwen3-coder', agent: AGENTS[2], tok: '52,044', ok: false },
                    ].map((r, i) => (
                      <tr key={r.id} className={cn('transition-colors hover:bg-surface-sunk', i > 0 && 'border-t border-border-subtle')}>
                        <td className="px-5 py-2.5 font-mono text-ink-2">{r.id}</td>
                        <td className="px-5 py-2.5">
                          <span className={cn('inline-flex items-center gap-1.5 rounded-[var(--r-pill)] px-2 py-0.5 text-[11px] font-medium', r.agent.soft, r.agent.text)}>
                            <span className={cn('h-1.5 w-1.5 rounded-full', r.agent.base)} />{r.model}
                          </span>
                        </td>
                        <td className="px-5 py-2.5 text-right font-mono tabular-nums text-ink">{r.tok}</td>
                        <td className="px-5 py-2.5 text-right">
                          <span className={cn('inline-flex items-center rounded-[var(--r-pill)] px-2 py-0.5 text-[11px] font-medium', r.ok ? 'bg-success-soft text-success-strong' : 'bg-danger-soft text-danger-strong')}>
                            {r.ok ? 'ok' : 'failed'}
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </Panel>

              {/* empty state */}
              <Panel className="flex flex-col items-center justify-center gap-3 py-12">
                <Inbox size={32} strokeWidth={1.5} className="text-ink-3" />
                <p className="text-[13px] text-ink-3">표시할 데이터가 없습니다.</p>
              </Panel>
            </div>
          </Section>

          {/* ============ DO / DON'T ============ */}
          <Section
            id="guidelines"
            n="07"
            icon={<ListChecks size={18} strokeWidth={1.75} />}
            title="Do's & Don'ts"
          >
            <div className="grid gap-4 lg:grid-cols-2">
              <div className="rounded-[var(--r-lg)] border border-border bg-success-soft p-[var(--pad-card)]">
                <h3 className="mb-3 inline-flex items-center gap-2 text-[13px] font-semibold text-success-strong"><Check size={15} strokeWidth={2.5} />Do</h3>
                <ul className="space-y-2 text-[12.5px] leading-relaxed text-ink-2">
                  {DOS.map((d) => <li key={d} className="flex gap-2"><span className="text-success-strong">✓</span>{d}</li>)}
                </ul>
              </div>
              <div className="rounded-[var(--r-lg)] border border-border bg-danger-soft p-[var(--pad-card)]">
                <h3 className="mb-3 inline-flex items-center gap-2 text-[13px] font-semibold text-danger-strong">Don&apos;t</h3>
                <ul className="space-y-2 text-[12.5px] leading-relaxed text-ink-2">
                  {DONTS.map((d) => <li key={d} className="flex gap-2"><span className="text-danger-strong">✕</span>{d}</li>)}
                </ul>
              </div>
            </div>
          </Section>

        </div>

        <footer className="mt-16 border-t border-border-subtle pt-6">
          <p className="font-mono text-[11px] text-ink-4">
            cctrace 디자인 시스템 · 단일 소스: <span className="text-ink-3">DESIGN.md</span> · 토큰: <span className="text-ink-3">app/globals.css</span> · 에이전트 색: <span className="text-ink-3">lib/colors.ts</span>
          </p>
        </footer>
      </div>
    </main>
  );
}
