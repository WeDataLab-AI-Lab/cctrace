// The local daemon's small, revision-bound projection of sessionfacts.Output. This is
// deliberately independent of the server's much larger SessionAnalysis response.
export type LocalAnalysisState = 'observed' | 'undeterminable' | 'truncated' | 'not_collected';

export interface LocalSessionAnalysis {
  revision: string;
  rules_version: number;
  state: LocalAnalysisState;
  session: {
    input_tokens: number | null;
    output_tokens: number | null;
    active_seconds: number | null;
    turns: number | null;
    inputs: number | null;
    cost_usd: number | null;
    turn_boundaries: { state: 'observed' | 'undeterminable'; reason?: string | null };
  } | null;
  turns: LocalAnalysisTurn[];
}

export interface LocalAnalysisTurn {
  turn_key: string;
  ordinal: number;
  status: 'ended' | 'interrupted' | 'error' | 'no_response' | 'open' | 'undeterminable';
  started_at: string;
  preview: string | null;
  preview_state: 'ok' | 'withheld' | 'redacted' | 'none';
  input_tokens: number | null;
  output_tokens: number | null;
  // sessionfacts does not publish a per-turn duration. Only render one when measured.
  active_seconds?: number | null;
}
