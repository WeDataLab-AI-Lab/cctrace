'use client';

import type { AnalysisRecaps, RecapAbsentReason } from '@/lib/session-analysis-types';
import { Chip } from './atoms';
import { formatDateTime } from './format';
import { RECAP_ABSENT_TEXT, RECAP_CONDITION_TEXT } from './labels';

/**
 * Claude Code가 자리를 비운 뒤 스스로 남긴 recap(`away_summary`)을 시간순으로 보인다(D12의 보조 출처).
 * 세션 기록이 없을 때 개요 자리를 대신하고, 기록이 있을 때는 접어서 둔다. 안내문은 서버가 떼고 같은 문장은 합쳐 보내므로
 * (`repeats`) 여기서는 그대로 그린다.
 *
 * recap이 없을 때는 빈칸이 아니라 왜 없는지를 적는다. 없다는 뜻이 아닌 경우(보류됨, 가려짐, 수집 이전)와
 * 정말 생길 조건이 안 된 경우(턴이 적음, 헤드리스, 자리 비운 적 없음)를 가른다.
 */

const absentReasonOf = (recaps: AnalysisRecaps): RecapAbsentReason | null => {
  if (recaps.absent_reason) return recaps.absent_reason;
  // 상태가 이유를 대신 말하는 경우.
  if (recaps.state === 'withheld' || recaps.state === 'redacted' || recaps.state === 'pre_collect') return recaps.state;
  return null;
};

/** 없다는 뜻이 아니라 "볼 수 없다"는 뜻인 이유들. */
const UNSEEN: ReadonlySet<RecapAbsentReason> = new Set(['withheld', 'redacted', 'pre_collect']);

interface RecapListProps {
  recaps: AnalysisRecaps;
  /** 상한(50개)에서 잘렸는가. */
  truncated: boolean;
  /** 본문 합계 상한(512KB)에 걸려 뒤의 본문이 비워졌는가(`truncated.bodies`). 비워진 recap은 글자가 없다. */
  bodiesTruncated?: boolean;
  /** recap은 Claude Code가 남기는 기록이다. 다른 에이전트는 "없음"이 아니라 "해당 없음"이다. */
  agent?: string;
}

const RecapList = ({ recaps, truncated, bodiesTruncated = false, agent }: RecapListProps) => {
  const items = [...recaps.items].sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts));
  const reason = absentReasonOf(recaps);
  return (
    <div>
      {items.length > 0 ? (
        <ol className="space-y-2">
          {items.map((item, index) => (
            <li key={`${item.ts}-${index}`} className="flex flex-col gap-0.5 @md:flex-row @md:gap-3">
              <time dateTime={item.ts} className="shrink-0 pt-px font-mono text-[10.5px] tabular-nums text-ink-3 @md:w-[78px]">
                {formatDateTime(item.ts)}
              </time>
              <p className="min-w-0 max-w-[78ch] text-[12px] leading-5 break-keep whitespace-pre-line text-ink-2 [overflow-wrap:anywhere]">
                {item.text.trim() === '' ? (
                  // 본문 상한이 뒤의 본문을 비웠다. 빈 줄로 두면 "recap 내용이 없었다"로 읽힌다.
                  <span className="text-ink-3 italic">
                    {bodiesTruncated ? '본문 합계 상한을 넘어 이 recap의 본문은 담기지 않았습니다.' : '이 recap의 본문을 받지 못했습니다.'}
                  </span>
                ) : (
                  item.text
                )}
                {item.repeats > 1 && (
                  <Chip tone="neutral" size="sm" className="ml-1.5 align-middle" title={`같은 문장이 ${item.repeats}번 남아 하나로 합쳤습니다.`}>
                    ×{item.repeats}
                  </Chip>
                )}
              </p>
            </li>
          ))}
        </ol>
      ) : agent === 'codex' ? (
        <p className="text-[12px] leading-relaxed text-ink-3">
          Codex 세션에는 recap이 없습니다. recap은 Claude Code가 자리를 비운 뒤 남기는 기록입니다.
        </p>
      ) : (
        <div className="space-y-1 text-[12px] leading-relaxed">
          <p className="text-ink-2">
            {reason ? (
              <>
                <span className="font-medium text-ink">{UNSEEN.has(reason) ? '볼 수 없습니다. ' : 'recap이 없습니다. '}</span>
                {RECAP_ABSENT_TEXT[reason]}
              </>
            ) : (
              <>
                <span className="font-medium text-ink">recap이 없습니다. </span>
                이 세션에서 recap이 남지 않은 이유는 기록되지 않았습니다.
              </>
            )}
          </p>
          <p className="text-[11px] text-ink-3">{RECAP_CONDITION_TEXT}</p>
        </div>
      )}
      {truncated && <p className="mt-1.5 text-[11px] text-ink-3">recap이 많아 앞의 일부만 담았습니다.</p>}
    </div>
  );
};

export { RecapList };
