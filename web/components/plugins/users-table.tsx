'use client';

import { Fragment, type MouseEvent } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { cn } from '@/lib/utils';
import { TableLoadingRows, rowIn } from '@/components/common/loading-rows';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { avgTokens, fmtTokens, totalCalls } from './usage-format';
import type { ByUserGroup } from './usage-types';

interface UsersTableProps {
  byUser: ByUserGroup[];
  expandedUser: string | null;
  onToggle: (user: string) => void;
  isLoading: boolean;
}

const UsersTable = ({ byUser, expandedUser, onToggle, isLoading }: UsersTableProps) => (
  <Table className="min-w-[980px]">
    <TableHeader className="bg-canvas text-ink-3 text-xs uppercase">
      <TableRow className="hover:bg-transparent">
        <TableHead className="px-4 py-3 text-left w-8"></TableHead>
        <TableHead className="px-4 py-3 text-left">User</TableHead>
        <TableHead className="px-4 py-3 text-right">Claude Calls</TableHead>
        <TableHead className="px-4 py-3 text-right">Codex Calls</TableHead>
        <TableHead className="px-4 py-3 text-right">Avg Tokens</TableHead>
        <TableHead className="px-4 py-3 text-right">Total</TableHead>
        <TableHead className="px-4 py-3 text-right">Input</TableHead>
        <TableHead className="px-4 py-3 text-right">Output</TableHead>
      </TableRow>
    </TableHeader>
    <TableBody className="divide-y divide-border/50">
      {isLoading ? (
        <TableLoadingRows columns={8} />
      ) : byUser.length === 0 ? (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={8} className="px-4 py-12 text-center text-ink-3">
            No plugin or skill usage data
          </TableCell>
        </TableRow>
      ) : (
        byUser.map((userRow, i) => {
          const isExpanded = expandedUser === userRow.user;
          const userCalls = userRow.pluginCalls + userRow.skillCalls;
          const handleToggle = () => onToggle(userRow.user);
          const handleButtonClick = (event: MouseEvent<HTMLButtonElement>) => {
            event.stopPropagation();
            handleToggle();
          };
          return (
            <Fragment key={userRow.user}>
              <TableRow
                onClick={handleToggle}
                className={cn(
                  'cursor-pointer transition-colors',
                  isExpanded ? 'bg-surface-sunk' : 'hover:bg-canvas',
                  rowIn(i),
                )}
              >
                <TableCell className="pl-4 py-3 text-ink-3">
                  {isExpanded ? <ChevronDown size={14} aria-hidden="true" /> : <ChevronRight size={14} aria-hidden="true" />}
                </TableCell>
                <TableCell className="px-4 py-1 font-medium text-ink">
                  <button type="button" aria-expanded={isExpanded} onClick={handleButtonClick} className="flex min-h-11 w-full items-center text-left hover:underline focus-visible:ring-2 focus-visible:ring-accent">{userRow.user}</button>
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-2">
                  {userRow.pluginCalls.toLocaleString()}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-2">
                  {userRow.skillCalls.toLocaleString()}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-2">
                  {avgTokens(userRow.totalTokens, userCalls)}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-2">
                  {fmtTokens(userRow.totalTokens)}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-3">
                  {fmtTokens(userRow.inputTokens)}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-3">
                  {fmtTokens(userRow.outputTokens)}
                </TableCell>
              </TableRow>
              {isExpanded && (
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={8} className="p-0 bg-surface-sunk">
                    <div className="px-6 pb-4 pt-1 overflow-x-auto">
                      <table className="w-full text-sm">
                        <thead>
                          <tr className="text-[11px] text-ink-3 border-b border-border">
                            <th className="py-1.5 text-left font-medium">Plugin / Skill</th>
                            <th className="py-1.5 text-right font-medium">Claude Calls</th>
                            <th className="py-1.5 text-right font-medium">Codex Calls</th>
                            <th className="py-1.5 text-right font-medium">Avg Tokens</th>
                            <th className="py-1.5 text-right font-medium">Total</th>
                            <th className="py-1.5 text-right font-medium">Input</th>
                            <th className="py-1.5 text-right font-medium">Output</th>
                          </tr>
                        </thead>
                        <tbody>
                          {userRow.items.map((row) => {
                            const rowCalls = totalCalls(row);
                            return (
                              <tr
                                key={row.name}
                                className="border-b border-border last:border-b-0"
                              >
                                <td className="py-2 font-mono text-[12px] text-ink">
                                  {row.name}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-2">
                                  {row.pluginCalls.toLocaleString()}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-2">
                                  {row.skillCalls.toLocaleString()}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-2">
                                  {avgTokens(row.totalTokens, rowCalls)}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-2">
                                  {fmtTokens(row.totalTokens)}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-3">
                                  {fmtTokens(row.inputTokens)}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-3">
                                  {fmtTokens(row.outputTokens)}
                                </td>
                              </tr>
                            );
                          })}
                        </tbody>
                      </table>
                    </div>
                  </TableCell>
                </TableRow>
              )}
            </Fragment>
          );
        })
      )}
    </TableBody>
  </Table>
);

export { UsersTable };
