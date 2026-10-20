'use client';

import { useState } from 'react';
import { ExternalLink, FilePenLine, GitPullRequest } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { AnalysisOutputs, AnalysisTurn, EditedFileItem, PullRequest } from '@/lib/session-analysis-types';
import { Chip, Hint, Unknown } from './atoms';
import { formatCount, formatDateTime, safeHref, splitPath } from './format';

/**
 * 변경한 파일(D11)의 펼친 내용: 편집한 파일 표, 추가·삭제 줄 수, PR. "세션 성과"의 셋째 줄이 펼칠 때 쓴다.
 *
 * 편집 횟수는 세 갈래로 따로 센다 — 활성 경로, 서브에이전트, 버려진 분기. 되감기가 파일을 되돌렸는지는
 * 알 수 없어서 버려진 분기의 편집을 빼지도 더하지도 않는다(p1b-design §3.15).
 */

const FILES_INITIAL = 8;

const sortFiles = (items: readonly EditedFileItem[]): EditedFileItem[] =>
  [...items].sort((a, b) => b.edits + b.subagent_edits - (a.edits + a.subagent_edits) || (a.path < b.path ? -1 : 1));

const FileRow = ({ item }: { item: EditedFileItem }) => {
  const { dir, base } = splitPath(item.path);
  return (
    <li className="flex items-center gap-2 py-1 text-[11.5px]">
      {/* 폴더는 줄어들어도 파일 이름은 끝까지 남는다. */}
      <span className="flex min-w-0 flex-1 items-baseline font-mono" title={item.path}>
        {dir && <span className="min-w-0 truncate text-ink-3">{dir}</span>}
        <span className="shrink-0 font-medium text-ink">{base}</span>
      </span>
      <span className="flex shrink-0 items-center gap-1.5 tabular-nums">
        <span className="text-ink" title="활성 경로의 편집 횟수">
          {formatCount(item.edits)}회
        </span>
        {item.subagent_edits > 0 && (
          <span className="text-ink-3" title="서브에이전트가 한 편집">
            서브 {formatCount(item.subagent_edits)}
          </span>
        )}
        {item.abandoned_edits > 0 && (
          <span className="text-ink-4" title="되감기로 버려진 분기에서 한 편집">
            버려짐 {formatCount(item.abandoned_edits)}
          </span>
        )}
      </span>
    </li>
  );
};

const EditedFilesBlock = ({ files, agent }: { files: AnalysisOutputs['edited_files']; agent?: string }) => {
  const [expanded, setExpanded] = useState(false);
  const sorted = sortFiles(files.items);
  const visible = expanded ? sorted : sorted.slice(0, FILES_INITIAL);
  const hidden = sorted.length - visible.length;
  const observedFileCount = new Set(files.items.map((item) => item.path)).size;
  // 이 에이전트는 편집을 수집하지 않는다: 합계가 "모름"인 이유가 한 가지라 합계 줄을 그리지 않는다.
  const notCollected = agent === 'codex' && files.items.length === 0;
  // 도구 비식별화로 경로만 가린 응답의 합계는 파일 수가 아니라 편집 호출 수다(p1b-design §3.15). "활성 N"으로 적으면
  // 파일 수로 읽히므로 이때만 머리 줄의 합계를 접고 안내 문장에 호출 수로 적는다. 합계가 null이면(프롬프트 비식별화)
  // 파일 수든 호출 수든 같은 "알 수 없음"이라 머리 줄에 그대로 둔다.
  const callCountOnly = files.state === 'redacted' && files.active_total != null;
  const showTotals = !notCollected && !callCountOnly;

  return (
    <div className="min-w-0">
      <div className="mb-1 flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
        <h4 className="flex items-center gap-1.5 text-[11.5px] font-semibold text-ink">
          <FilePenLine size={12} strokeWidth={1.75} className="text-ink-3" aria-hidden="true" />
          편집한 파일
        </h4>
        {showTotals && (
          <>
            {files.state === 'observed' && (
              <span className="text-[11px] text-ink-2">
                {files.truncated ? '확인한 파일' : '파일'}{' '}
                <b className="font-semibold tabular-nums text-ink">{formatCount(observedFileCount)}개</b>
              </span>
            )}
            <span className="text-[11px] text-ink-2">
              활성 편집{' '}
              {files.active_total == null ? <Unknown reason={files.state === 'redacted' ? '프롬프트 비식별화로 편집 호출 수도 알 수 없습니다.' : '활성 편집 횟수 합계를 알 수 없습니다.'} /> : (
                <b className="font-semibold tabular-nums text-ink">{formatCount(files.active_total)}회</b>
              )}
            </span>
            <span className="text-[11px] text-ink-2">
              <Hint label="되감기가 파일을 되돌렸는지 알 수 없어 버려진 분기의 편집을 빼지도 더하지도 않고 따로 셉니다." className="underline decoration-dotted underline-offset-2">
                버려진 분기 편집
              </Hint>{' '}
              {files.abandoned_total == null ? <Unknown /> : (
                <b className="font-semibold tabular-nums text-ink-2">{formatCount(files.abandoned_total)}회</b>
              )}
            </span>
          </>
        )}
      </div>

      {notCollected ? (
        <p className="rounded bg-surface-sunk px-2.5 py-2 text-[11.5px] leading-relaxed text-ink-3">
          Codex 세션은 편집한 파일을 수집하지 않습니다. 편집이 없었다는 뜻이 아닙니다.
        </p>
      ) : files.state === 'redacted' ? (
        <p className="rounded bg-surface-sunk px-2.5 py-2 text-[11.5px] leading-relaxed text-ink-3">
          {files.active_total == null ? (
            '프롬프트 비식별화로 편집한 경로도 편집 호출 수도 알 수 없습니다. 편집이 없었다는 뜻이 아닙니다.'
          ) : (
            <>
              도구 비식별화로 편집한 경로가 가려져 파일 이름을 볼 수 없습니다. 편집 호출은{' '}
              <b className="font-semibold tabular-nums text-ink-2">{formatCount(files.active_total)}</b>회 있었습니다.
              {files.abandoned_total != null && files.abandoned_total > 0 && (
                <> 되감기로 버려진 분기에서 한 호출은 따로 {formatCount(files.abandoned_total)}회입니다.</>
              )}
            </>
          )}
        </p>
      ) : sorted.length === 0 ? (
        <p className="py-1 text-[11.5px] text-ink-3">편집한 파일이 없습니다.</p>
      ) : (
        <>
          <ul className="divide-y divide-border-subtle">
            {visible.map((item) => (
              <FileRow key={item.path} item={item} />
            ))}
          </ul>
          {sorted.length > FILES_INITIAL && (
            // 하나의 버튼이 글자만 바꾼다. 누른 버튼이 사라지면 키보드 포커스가 문서 맨 위로 돌아간다.
            <button
              type="button"
              aria-expanded={expanded}
              onClick={() => setExpanded((v) => !v)}
              className="mt-1 rounded px-1 py-0.5 text-[11px] font-medium text-ink-2 underline decoration-dotted underline-offset-2 hover:text-ink"
            >
              {expanded ? '접기' : `${formatCount(hidden)}개 더 보기`}
            </button>
          )}
        </>
      )}
      {files.truncated && files.state === 'observed' && !notCollected && (
        <p className="mt-1.5 text-[11px] text-ink-3">
          경로 목록이 잘려 전체 파일 수를 알 수 없습니다. 확인한 파일 {formatCount(observedFileCount)}개입니다.{' '}
          {files.active_total == null ? '활성 편집 횟수 합계도 알 수 없습니다.' : '활성 편집 횟수는 전체 기준입니다.'}
        </p>
      )}
    </div>
  );
};

const LinesBlock = ({ lines, agent }: { lines: AnalysisOutputs['lines']; agent?: string }) => (
  <div>
    <h4 className="mb-1 text-[11.5px] font-semibold text-ink">추가·삭제 줄</h4>
    {!lines ? (
      <Unknown
        reason={
          agent === 'codex'
            ? 'Codex는 추가·삭제 줄 수를 남기지 않습니다.'
            : 'Claude Code가 남긴 세션 비용 기록(cost-state)이 없어 줄 수를 알 수 없습니다.'
        }
        label={agent === 'codex' ? '수집 안 함' : '기록 없음'}
      />
    ) : (
      <p className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-[13px] font-semibold tabular-nums">
        <span className="text-success-strong">+{formatCount(lines.added)}</span>
        <span className="text-danger-strong">−{formatCount(lines.removed)}</span>
        {lines.partial && (
          <Hint label="세션 비용 기록의 일부가 상한에 걸려 빠졌습니다. 실제 값은 이보다 클 수 있습니다." className="font-normal">
            <Chip tone="unknown" size="sm">
              일부
            </Chip>
          </Hint>
        )}
      </p>
    )}
  </div>
);

const PrRow = ({ pr, requestNo }: { pr: PullRequest; requestNo: number | null }) => {
  const href = safeHref(pr.url);
  const label = (
    <>
      <GitPullRequest size={12} strokeWidth={1.75} className="shrink-0 text-ink-3" aria-hidden="true" />
      <span className="font-semibold tabular-nums">#{pr.number}</span>
      <span className="truncate font-mono text-ink-3">{pr.repository}</span>
      {href && <ExternalLink size={10} strokeWidth={1.75} className="shrink-0 text-ink-4" aria-hidden="true" />}
    </>
  );
  return (
    <li className="flex flex-wrap items-center gap-x-2 gap-y-0.5 py-1 text-[11.5px]">
      {href ? (
        <a
          href={href}
          target="_blank"
          rel="noopener noreferrer"
          className={cn('inline-flex min-w-0 items-center gap-1 rounded text-ink hover:underline')}
          aria-label={`PR ${pr.number} ${pr.repository} 새 창에서 열기`}
        >
          {label}
        </a>
      ) : (
        <span className="inline-flex min-w-0 items-center gap-1 text-ink">{label}</span>
      )}
      <span className="text-[10.5px] text-ink-3">
        {requestNo != null ? `요청 #${requestNo}에서 · ` : ''}
        {formatDateTime(pr.first_seen_at)}
      </span>
    </li>
  );
};

const PrBlock = ({ prs, turns }: { prs: AnalysisOutputs['prs']; turns: readonly AnalysisTurn[] }) => {
  const requestOf = (turnKey: string | null): number | null =>
    turnKey ? (turns.find((t) => t.key === turnKey)?.request_no ?? null) : null;
  return (
    <div>
      <h4 className="mb-1 flex items-center gap-1.5 text-[11.5px] font-semibold text-ink">
        PR
        <span className="font-normal tabular-nums text-ink-3">{prs.items.length > 0 ? formatCount(prs.items.length) : ''}</span>
      </h4>
      {prs.items.length === 0 ? (
        <p className="text-[11.5px] text-ink-3">이 세션에서 만든 PR이 기록되지 않았습니다.</p>
      ) : (
        <ul className="divide-y divide-border-subtle">
          {prs.items.map((pr) => (
            <PrRow key={pr.url} pr={pr} requestNo={requestOf(pr.turn_key)} />
          ))}
        </ul>
      )}
      {prs.truncated && <p className="mt-1 text-[11px] text-ink-3">PR이 많아 일부만 담았습니다.</p>}
    </div>
  );
};

export { EditedFilesBlock, LinesBlock, PrBlock };
