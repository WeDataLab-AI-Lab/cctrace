/**
 * cctrace color system — agent-equal, vendor-neutral (DESIGN.md v0602).
 *
 * Principle: every model resolves to its AGENT's hue, shaded by tier
 * (a better model = a deeper shade within the same hue). There is no
 * "non-Claude → gray" bias — an unknown model falls back to its agent's
 * MID shade, never gray.
 *
 * Returned values are either raw OKLCH (per-model shades) or `var(--token)`
 * (agent / categorical / neutral). Both are valid SVG fill/stroke values,
 * so Recharts consumes them directly.
 */

export type Agent = 'claude' | 'codex' | 'compat' | 'gjc' | 'omo';

/** Infer the agent family from a (possibly shortened / versioned) model string. */
export function agentOf(model: string): Agent {
  const m = model.toLowerCase();
  if (m.includes('claude') || m.includes('fable') || m.includes('opus') || m.includes('sonnet') || m.includes('haiku')) {
    return 'claude';
  }
  if (m.includes('gpt') || m.includes('codex') || isCodexModel(m) || m.includes('reason')) {
    return 'codex';
  }
  return 'compat';
}

/**
 * Per-model shade within the agent's hue. Better model → deeper (darker, more
 * saturated). Unknown tiers fall back to the agent's mid shade (never gray).
 */
function modelShade(model: string): string {
  const m = model.toLowerCase();
  switch (agentOf(model)) {
    case 'claude':
      // Fable is the flagship: one flat colour, deepest in the Claude hue.
      // It used to have a second, gradient channel for badges and dots; that
      // gradient was standing in for a shade ladder the anchor did not actually
      // sit at the bottom of, so the anchor was deepened and the channel dropped.
      if (m.includes('fable')) return 'var(--model-fable)';
      // Hue 39 throughout: Anthropic's coral, the same hue --agent-claude carries.
      // Tier is expressed by lightness alone, so a model always reads as its vendor
      // first and its tier second.
      //
      // Chroma sits below the brand value on purpose. These fills cover large areas
      // of a stacked chart, where a colour at full brand strength stops being an
      // identity and becomes a glare — the hue is what says "Anthropic", and it says
      // it just as clearly at two thirds the saturation.
      if (m.includes('opus')) return 'oklch(0.66 0.127 39)'; // vivid terracotta, lifted off the floor
      if (m.includes('haiku')) return 'oklch(0.80 0.068 39)'; // lightest
      return 'oklch(0.73 0.096 39)'; // sonnet / claude mid — brand hue, muted for fills
    case 'codex':
      // Astra is the GPT-6 flagship: deepest in the capri ramp, above Sol.
      // Hue 232 throughout — Codex's own accent, see --agent-codex.
      if (m.includes('gpt-6-astra')) return 'var(--model-astra)';
      // Sol was the flagship anchor before Astra; it keeps its former depth as a
      // raw shade, now the deepest one in the ramp below Astra's token.
      if (m.includes('gpt-5.6-sol')) return 'oklch(0.55 0.135 232)';
      if (m.includes('gpt-5.6-terra')) return 'oklch(0.65 0.122 232)';
      if (m.includes('gpt-5.6-luna')) return 'oklch(0.77 0.099 232)';
      if (m.includes('mini')) return 'oklch(0.75 0.101 232)';
      if (m.includes('gpt-5.5') || m.includes('gpt-5-codex')) return 'oklch(0.63 0.125 232)';
      if (m.includes('gpt-5.4')) return 'oklch(0.66 0.120 232)';
      if (m.includes('gpt-5.3')) return 'oklch(0.70 0.113 232)';
      if (m.includes('gpt-5.2')) return 'oklch(0.73 0.107 232)';
      if (m.includes('codex')) return 'oklch(0.63 0.125 232)';
      if (isCodexModel(m) || m.includes('reason')) return 'oklch(0.84 0.072 232)'; // o-series reasoning, lightest
      return 'oklch(0.80 0.105 232)'; // codex mid — brand hue, muted for fills
    default:
      if (m.includes('qwen')) return 'oklch(0.63 0.095 182)'; // deeper
      if (m.includes('glm')) return 'oklch(0.74 0.073 182)'; // lighter
      return 'oklch(0.69 0.090 182)'; // compat mid
  }
}

/** Fable gets gradient treatment in badge/dot/<defs> contexts (chart fills stay solid). */
export function isFable(model: string): boolean {
  return model.toLowerCase().includes('fable');
}

/** True for the GPT-6 Astra flagship tier. */
export function isAstra(model: string): boolean {
  const m = model.toLowerCase();
  return m.includes('gpt-6-astra') || m === 'astra';
}

/** True for OpenAI o-series reasoning models (o1, o3, o4-mini, …). */
function isCodexModel(model: string): boolean {
  return /\bo[1-9]/.test(model.toLowerCase());
}

/** Family-consistent model color — single source of truth across all charts. */
export function modelColor(model: string): string {
  if (!model) return 'var(--ink-3)';
  const m = model.toLowerCase();
  // "Others" groups non-primary model spend; keep it in the compatible hue.
  if (m === 'others' || m === 'other') return 'var(--agent-compat)';
  return modelShade(model);
}

/** Colorblind-safe categorical palette — distinct color per user by index. */
const USER_CATS = [
  'var(--cat-1)', 'var(--cat-2)', 'var(--cat-3)', 'var(--cat-4)',
  'var(--cat-5)', 'var(--cat-6)', 'var(--cat-7)', 'var(--cat-8)',
];

/** Assign a distinct color per user by index (stable, colorblind-safe). */
export function userColor(index: number): string {
  return USER_CATS[index % USER_CATS.length];
}

/** Per-user series colors. The weekly report line is not a person, so it keeps its
 * own token and takes no palette slot: the people after it keep the colors they
 * would have without it. */
export function userSeriesColorMap(userKeys: readonly string[]): Record<string, string> {
  const colors: Record<string, string> = {};
  let person = 0;
  for (const key of userKeys) {
    colors[key] = key === 'weekly' ? 'var(--agent-weekly)' : userColor(person++);
  }
  return colors;
}

export type ModelCategory = 'all' | 'anthropic' | 'codex' | 'compatible';

/** Column-based category match — single source of truth for filtering rows by ModelCategory.
 * Use this instead of name-based heuristics whenever a row carries agent/billing_provider.
 *
 * Mirrors the server's billing_provider-based axis (internal/store/postgres_stats.go and
 * its siblings) — the axis is billing_provider, not agent. gjc and omo each mix
 * subscription-billed and non-subscription traffic inside a single agent, so an
 * agent-based bucket can't be correct for them. Keep both sides in sync: if a server
 * WHERE clause here changes, mirror it in colors.ts, and vice versa.
 *   anthropic  -> billing_provider = 'anthropic'
 *   codex      -> billing_provider = 'openai'
 *   compatible -> billing_provider NOT IN ('anthropic', 'openai')
 */
export function matchCategory(agent: string | undefined, billingProvider: string | undefined, cat: ModelCategory): boolean {
  if (cat === 'all') return true;
  const bp = billingProvider ?? 'anthropic';
  if (cat === 'anthropic') return bp === 'anthropic';
  if (cat === 'codex') return bp === 'openai';
  if (cat === 'compatible') return bp !== 'anthropic' && bp !== 'openai';
  return true;
}

/** Harness-scope match — the client-side twin of appendAgentScope in
 * internal/store/postgres_session_records.go.
 *
 * The scope vocabulary and the stored vocabulary differ: the pills say 'other', rows
 * say 'omo' or 'gjc', and no row is ever stored as 'other'. Comparing the two directly
 * silently drops every non-Claude, non-Codex harness, which is how Overview read $0 for
 * a scope whose Sessions tab listed real sessions. Keep both sides in sync.
 *   claude/codex/weekly -> agent = <scope>
 *   other               -> agent NOT IN ('claude', 'codex', 'weekly')
 */
export function matchAgentScope(agent: string | undefined, scope: string): boolean {
  if (!scope) return true;
  const a = agent ?? 'claude';
  if (scope === 'other') return a !== 'claude' && a !== 'codex' && a !== 'weekly';
  return a === scope;
}
