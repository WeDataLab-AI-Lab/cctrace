/**
 * Monthly subscription prices, keyed by the plan identifier the client already
 * detects.
 *
 * These are in code rather than in a settings form because nothing about them
 * is per-installation guesswork: the identifier on the left is read from the
 * machine (Claude's rateLimitTier, Codex's plan_type), so the only thing a
 * person could add is the published list price — the same number for everyone.
 * Asking each operator to type it in invited them to guess which row their
 * account was, which is exactly the ambiguity the detected identifier removes.
 *
 * A plan that is not listed here is NOT free. It is unpriced: the chart draws
 * its line, leaves it out of the weighted average, and names it in the coverage
 * note. Adding a fabricated number here would be worse than the gap, because
 * the average would keep being drawn and would quietly be wrong.
 */

interface PlanPricing {
  /** Human-readable name for the coverage note and per-account legend. */
  label: string;
  monthlyUSD: number;
}

/**
 * Keys are `${billing_provider}:${plan}` where plan is the value stored on
 * quota_samples — Claude's rateLimitTier, Codex's plan_type.
 *
 * The multiplier in a Claude tier (5x, 20x) is a quota multiple, not a price
 * multiple: 20x is four times the quota of 5x but twice the price. Prices are
 * therefore listed, never derived from the tier string.
 */
const PLAN_PRICES: Record<string, PlanPricing> = {
  'anthropic:default_claude_max_20x': { label: 'Claude Max 20x', monthlyUSD: 200 },
  'anthropic:default_claude_max_5x': { label: 'Claude Max 5x', monthlyUSD: 100 },
  'openai:pro': { label: 'Codex Pro', monthlyUSD: 200 },
  'openai:prolite': { label: 'Codex Pro Lite', monthlyUSD: 100 },
  'openai:plus': { label: 'ChatGPT Plus', monthlyUSD: 20 },
};

const planKey = (provider: string, plan: string) => `${provider}:${plan}`;

/** priceOf returns the monthly price, or 0 when the plan is not listed. */
const priceOf = (provider: string, plan: string): number =>
  PLAN_PRICES[planKey(provider, plan)]?.monthlyUSD ?? 0;

/**
 * planLabel names a plan for display, falling back to the raw identifier.
 *
 * The fallback is deliberate: an unrecognised tier should appear on screen as
 * the string the machine actually reported, so it can be looked up and added
 * here rather than hidden behind a generic label.
 */
const planLabel = (provider: string, plan: string): string =>
  PLAN_PRICES[planKey(provider, plan)]?.label ?? plan;

export { planLabel, priceOf };
export type { PlanPricing };
