'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { listUnpricedModels, markFlatRateModel, unmarkFlatRateModel } from '@/lib/api';
import { cn } from '@/lib/utils';
import { CollectingLoader } from '@/components/common/collecting-loader';
import { useAuth } from '@/components/common/auth-context';
import type { FlatRateModel, UnpricedModel, UnpricedModelsResponse } from '@/lib/types';

const UNPRICED_KEY = ['unpriced-models'] as const;

const GRID_CLASS = 'grid grid-cols-[90px_1.6fr_90px_1.2fr_1.2fr_120px] items-center';
const FLAT_GRID_CLASS = 'grid grid-cols-[90px_1.6fr_1.4fr_1fr_120px] items-center';
const ROW_CLASS = 'px-4 py-3 border-b border-surface-sunk last:border-b-0 hover:bg-canvas transition-colors';
const HEAD_CLASS = 'px-4 py-2.5 border-b border-surface-sunk bg-canvas';
const ACTION_CLASS = 'text-[11px] font-medium text-brand hover:underline disabled:opacity-50';

const fmtDate = (ts: string): string => new Date(ts).toLocaleDateString();

const UnpricedRow = ({ model }: { model: UnpricedModel }) => {
  const queryClient = useQueryClient();
  // The mark changes no cost, only this list, so only this list is refreshed.
  const mutation = useMutation({
    mutationFn: () => markFlatRateModel(model.agent, model.model, ''),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: UNPRICED_KEY }),
  });
  const handleMark = () => mutation.mutate();
  const total = model.input_tokens + model.output_tokens;

  return (
    <div className={cn(GRID_CLASS, ROW_CLASS)}>
      <span className="text-[12px] text-ink-2 truncate pr-2">{model.agent}</span>
      {model.model ? (
        <span className="text-[13px] text-ink font-mono truncate pr-2 select-all">{model.model}</span>
      ) : (
        <span className="text-[13px] text-ink-3 truncate pr-2">(empty model)</span>
      )}
      <span className="text-[12px] text-ink text-right tabular-nums pr-2">{model.rows.toLocaleString()}</span>
      <span className="min-w-0 pr-2 text-right">
        <span className="block text-[12px] text-ink tabular-nums">{total.toLocaleString()}</span>
        <span className="block text-[11px] text-ink-3 tabular-nums truncate">
          in {model.input_tokens.toLocaleString()} · out {model.output_tokens.toLocaleString()} · cache{' '}
          {model.cache_read_tokens.toLocaleString()}
        </span>
      </span>
      <span className="text-[12px] text-ink-3 whitespace-nowrap pr-2">
        {fmtDate(model.first_ts)} – {fmtDate(model.last_ts)}
      </span>
      <span className="text-right">
        {/* A blank model is a collection gap to fix, not a model to declare free;
            the server refuses the mark, so the button is not offered. */}
        {model.model ? (
          <button type="button" onClick={handleMark} disabled={mutation.isPending} className={ACTION_CLASS}>
            Mark flat-rate
          </button>
        ) : (
          <span className="text-[11px] text-ink-3">—</span>
        )}
        {mutation.error && <span className="block text-[11px] text-danger-strong">{mutation.error.message}</span>}
      </span>
    </div>
  );
};

const FlatRateRow = ({ model }: { model: FlatRateModel }) => {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: () => unmarkFlatRateModel(model.agent, model.model),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: UNPRICED_KEY }),
  });
  const handleUnmark = () => mutation.mutate();

  return (
    <div className={cn(FLAT_GRID_CLASS, ROW_CLASS)}>
      <span className="text-[12px] text-ink-2 truncate pr-2">{model.agent}</span>
      <span className="text-[13px] text-ink font-mono truncate pr-2">{model.model}</span>
      <span className="text-[12px] text-ink-2 truncate pr-2">{model.reason || '—'}</span>
      <span className="text-[12px] text-ink-3 truncate pr-2">{model.created_by || '—'}</span>
      <span className="text-right">
        <button type="button" onClick={handleUnmark} disabled={mutation.isPending} className={ACTION_CLASS}>
          Unmark
        </button>
        {mutation.error && <span className="block text-[11px] text-danger-strong">{mutation.error.message}</span>}
      </span>
    </div>
  );
};

const UnpricedModelsTab = () => {
  const { isAdmin } = useAuth();
  // No polling: the list moves only when new usage arrives or an admin marks a
  // model here, and a mark already invalidates it.
  const { data, isLoading } = useQuery<UnpricedModelsResponse>({
    queryKey: UNPRICED_KEY,
    queryFn: listUnpricedModels,
    enabled: isAdmin,
  });

  if (!isAdmin) {
    return <div className="px-4 py-6 text-sm text-ink-3">관리자 전용 페이지입니다.</div>;
  }

  const unpriced = data?.unpriced ?? [];
  const flatRate = data?.flat_rate ?? [];

  return (
    <div className="space-y-4">
      <header>
        <h2 className="text-[16px] font-semibold text-ink">Unpriced Models</h2>
        <p className="mt-0.5 text-[11px] text-ink-3">
          Token usage no rate matched. Cost figures count it as $0, so every total that includes it is low. Add a
          rate for the model, or mark it flat-rate if $0 is correct (local models, subscriptions).
        </p>
      </header>

      <div className="bg-surface border border-border rounded-lg overflow-x-auto">
        <div className="min-w-[820px]">
          <header className={cn(GRID_CLASS, HEAD_CLASS)}>
            <span className="text-[11px] font-medium text-ink-3">Agent</span>
            <span className="text-[11px] font-medium text-ink-3">Model</span>
            <span className="text-[11px] font-medium text-ink-3 text-right pr-2">Rows</span>
            <span className="text-[11px] font-medium text-ink-3 text-right pr-2">Tokens</span>
            <span className="text-[11px] font-medium text-ink-3">Seen</span>
            <span className="text-[11px] font-medium text-ink-3 text-right">Action</span>
          </header>

          {isLoading && <CollectingLoader className="py-6" />}

          {!isLoading && unpriced.length === 0 && (
            <div className="px-4 py-6 text-center text-sm text-ink-3">Every model with usage has a rate</div>
          )}

          {unpriced.map((m) => (
            <UnpricedRow key={`${m.agent}/${m.model}`} model={m} />
          ))}
        </div>
      </div>

      <header className="pt-2">
        <h2 className="text-[16px] font-semibold text-ink">Flat-rate Models</h2>
        <p className="mt-0.5 text-[11px] text-ink-3">Marked as $0 by design. Unmark to list them as unpriced again.</p>
      </header>

      <div className="bg-surface border border-border rounded-lg overflow-x-auto">
        <div className="min-w-[680px]">
          <header className={cn(FLAT_GRID_CLASS, HEAD_CLASS)}>
            <span className="text-[11px] font-medium text-ink-3">Agent</span>
            <span className="text-[11px] font-medium text-ink-3">Model</span>
            <span className="text-[11px] font-medium text-ink-3">Reason</span>
            <span className="text-[11px] font-medium text-ink-3">Marked By</span>
            <span className="text-[11px] font-medium text-ink-3 text-right">Action</span>
          </header>

          {!isLoading && flatRate.length === 0 && (
            <div className="px-4 py-6 text-center text-sm text-ink-3">No models marked flat-rate</div>
          )}

          {flatRate.map((m) => (
            <FlatRateRow key={`${m.agent}/${m.model}`} model={m} />
          ))}
        </div>
      </div>
    </div>
  );
};

export { UnpricedModelsTab };
