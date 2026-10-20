'use client';

import type { ReactNode } from 'react';
import type { SessionOverview } from '@/lib/types';
import type { SessionGroupMember, SessionGroups } from '@/lib/session-groups-api';
import { RunningGroupHeader } from './analysis/list-groups';

type EndedGroup = SessionGroups['ended'][number];

export function partitionSessionGroups(rows: readonly SessionOverview[], groups?: SessionGroups) {
  const byId = new Map(rows.map((row) => [row.session_id, row]));
  const running = groups?.running.flatMap(({ session_id }) => byId.get(session_id) ?? []) ?? [];
  const used = new Set(running.map((row) => row.session_id));
  const ended = (groups?.ended ?? []).map((group) => {
    const members = group.items.filter((member) => !member.dismissed && byId.has(member.session_id));
    for (const member of members) used.add(member.session_id);
    return { group, members: members.map((member) => ({ member, row: byId.get(member.session_id)! })) };
  }).filter(({ group, members }) => members.length > 0 || group.items.some((member) => !member.dismissed) || group.items.length < group.total);
  return { running, ended, rest: rows.filter((row) => !used.has(row.session_id)) };
}

interface SessionGroupListProps {
  rows: readonly SessionOverview[];
  groups?: SessionGroups;
  busy: boolean;
  renderRow: (row: SessionOverview, action?: ReactNode) => ReactNode;
  onDismiss: (members: readonly SessionGroupMember[], group?: EndedGroup, allMembers?: boolean) => void;
  onRestore: (members: readonly SessionGroupMember[], group?: EndedGroup) => void;
  /** Keep the rows in server order (analysis query/plan/category/sort) instead of regrouping them. */
  preserveOrder?: boolean;
}

export function SessionGroupList({ rows, groups, busy, renderRow, onDismiss, onRestore, preserveOrder = false }: SessionGroupListProps) {
  const { running, ended, rest } = partitionSessionGroups(rows, groups);
  const runningBy = groups?.counts.running_by;
  const counts = {
    total: groups?.counts.running ?? 0,
    processing: runningBy?.processing ?? 0,
    waiting: runningBy?.waiting ?? 0,
    done: runningBy?.done ?? 0,
    interrupted: runningBy?.interrupted ?? 0,
    leftover: runningBy?.leftover ?? 0,
    unknown: runningBy?.unknown ?? 0,
  };
  const endedHeader = (group: EndedGroup, loaded: number) => (
    <div role="group" aria-label={`함께 끝난 세션 ${group.total}건`} className="border-b border-border bg-surface-sunk px-3 py-2 text-[11px] text-ink-2">
      <div className="font-medium">{new Date(group.ended_at).toLocaleString('ko-KR')}에 함께 끝난 {group.total}건</div>
      <div>현재 목록 {loaded}건 표시 · 서버 반환 {group.items.length}건{groups?.truncated ? ' · 서버 반환 한도 도달' : ''}</div>
      <button type="button" className="mt-1 rounded border border-border px-1.5 py-0.5 disabled:opacity-50" disabled={busy || group.items.every((member) => member.dismissed) && group.items.length === group.total} onClick={() => onDismiss(group.items.filter((member) => !member.dismissed), group, true)}>
        한꺼번에 떼기
      </button>
      {group.items.length < group.total && <div>서버에서 나머지 멤버를 확인한 뒤 전체 떼기를 적용합니다.</div>}
      {preserveOrder && <div>현재 검색·플랜·범주·기간·프로젝트·계정 필터가 묶음과 전체 떼기 대상에 적용됩니다.</div>}
    </div>
  );
  const restoreAction = (row: SessionOverview) => {
    const group = groups?.ended.find((entry) => entry.items.some((item) => item.session_id === row.session_id && item.dismissed));
    const member = group?.items.find((item) => item.session_id === row.session_id && item.dismissed);
    return member && <button type="button" className="rounded border border-border px-1 py-0.5 text-[10px] disabled:opacity-50" disabled={busy} onClick={() => onRestore([member], group)}>묶음 복원</button>;
  };
  if (preserveOrder) {
    // Headers stay together above the rows; each row keeps its own group action in place.
    const memberOf = new Map(ended.flatMap(({ group, members }) => members.map(({ member }) => [member.session_id, { group, member }] as const)));
    return <>
      {groups && <div className="border-b border-border px-3 py-2 text-[11px] text-ink-3">전체 범위 {groups.overview_total.toLocaleString()}건 · 실행 {groups.counts.running.toLocaleString()}건 · 종료 {groups.counts.ended.toLocaleString()}건 · 현재 목록 {rows.length.toLocaleString()}건{groups.truncated ? ' · 서버 반환 한도 도달, 일부 묶음 미표시' : ''} · 현재 분석 필터 모집단에서 계산했으며 행은 목록 정렬 순서를 유지합니다.</div>}
      {running.length > 0 && <RunningGroupHeader counts={counts} />}
      {ended.map(({ group, members }) => <div key={group.key}>{endedHeader(group, members.length)}</div>)}
      {rows.map((row) => {
        const entry = memberOf.get(row.session_id);
        return renderRow(row, entry ? <span className="inline-flex items-center gap-1 text-[10px] text-ink-3">{new Date(entry.group.ended_at).toLocaleString('ko-KR')} 묶음<button type="button" className="rounded border border-border px-1 py-0.5 disabled:opacity-50" disabled={busy} onClick={() => onDismiss([entry.member], entry.group)}>떼기</button></span> : restoreAction(row));
      })}
    </>;
  }
  return <>
    {groups && <div className="border-b border-border px-3 py-2 text-[11px] text-ink-3">전체 범위 {groups.overview_total.toLocaleString()}건 · 실행 {groups.counts.running.toLocaleString()}건 · 종료 {groups.counts.ended.toLocaleString()}건 · 현재 목록 {rows.length.toLocaleString()}건{groups.truncated ? ' · 서버 반환 한도 도달, 일부 묶음 미표시' : ''}</div>}
    {running.length > 0 && <RunningGroupHeader counts={counts} />}
    {running.map((row) => renderRow(row))}
    {ended.map(({ group, members }) => <div key={group.key}>{endedHeader(group, members.length)}{members.map(({ member, row }) => renderRow(row, <button type="button" className="rounded border border-border px-1 py-0.5 text-[10px] disabled:opacity-50" disabled={busy} onClick={() => onDismiss([member], group)}>떼기</button>))}</div>)}
    {rest.map((row) => renderRow(row, restoreAction(row)))}
  </>;
}
