'use client';

import { Fragment } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { cn } from '@/lib/utils';
import { CollectingLoader } from '@/components/common/collecting-loader';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { avgTokens, fmtTokens, totalCalls } from './usage-format';
import type { CombinedUsage, UserUsage } from './usage-types';

interface SkillsTableProps {
  rows: CombinedUsage[];
  bySkillUser: Map<string, UserUsage[]>;
  expandedSkill: string | null;
  onToggle: (name: string) => void;
  isLoading: boolean;
}

const SkillsTable = ({
  rows,
  bySkillUser,
  expandedSkill,
  onToggle,
  isLoading,
}: SkillsTableProps) => (
  <Table className="min-w-[980px]">
    <TableHeader className="bg-canvas text-ink-3 text-xs uppercase">
      <TableRow className="hover:bg-transparent">
        <TableHead className="px-4 py-3 text-left w-8"></TableHead>
        <TableHead className="px-4 py-3 text-left">Plugin / Skill</TableHead>
        <TableHead className="px-4 py-3 text-right">Claude Calls</TableHead>
        <TableHead className="px-4 py-3 text-right">Codex Calls</TableHead>
        <TableHead className="px-4 py-3 text-right">Avg Tokens</TableHead>
        <TableHead className="px-4 py-3 text-right">Total</TableHead>
        <TableHead className="px-4 py-3 text-right">Input</TableHead>
        <TableHead className="px-4 py-3 text-right">Output</TableHead>
        <TableHead className="px-4 py-3 text-right">Users</TableHead>
      </TableRow>
    </TableHeader>
    <TableBody className="divide-y divide-border/50">
      {isLoading ? (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={9} className="px-4 py-8 text-center text-ink-3">
            <CollectingLoader />
          </TableCell>
        </TableRow>
      ) : rows.length === 0 ? (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={9} className="px-4 py-12 text-center text-ink-3">
            No plugin or skill usage data
          </TableCell>
        </TableRow>
      ) : (
        rows.map((row) => {
          const rowCalls = totalCalls(row);
          const users = bySkillUser.get(row.name) ?? [];
          const isExpanded = expandedSkill === row.name;
          return (
            <Fragment key={row.name}>
              <TableRow
                onClick={() => onToggle(row.name)}
                className={cn(
                  'cursor-pointer transition-colors',
                  isExpanded ? 'bg-surface-sunk' : 'hover:bg-canvas'
                )}
              >
                <TableCell className="pl-4 py-3 text-ink-3">
                  {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                </TableCell>
                <TableCell className="px-4 py-3 font-mono text-[12px] text-ink">
                  {row.name}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-2">
                  {row.pluginCalls.toLocaleString()}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-2">
                  {row.skillCalls.toLocaleString()}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-2">
                  {avgTokens(row.totalTokens, rowCalls)}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-2">
                  {fmtTokens(row.totalTokens)}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-3">
                  {fmtTokens(row.inputTokens)}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-3">
                  {fmtTokens(row.outputTokens)}
                </TableCell>
                <TableCell className="px-4 py-3 text-right text-ink-3">
                  {row.users.size || '-'}
                </TableCell>
              </TableRow>
              {isExpanded && (
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={9} className="p-0 bg-surface-sunk">
                    <div className="px-6 pb-4 pt-1 overflow-x-auto">
                      <table className="w-full min-w-[760px] text-sm">
                        <thead>
                          <tr className="text-[11px] text-ink-3 border-b border-border">
                            <th className="py-1.5 text-left font-medium">User</th>
                            <th className="py-1.5 text-right font-medium">Claude Calls</th>
                            <th className="py-1.5 text-right font-medium">Codex Calls</th>
                            <th className="py-1.5 text-right font-medium">Avg Tokens</th>
                            <th className="py-1.5 text-right font-medium">Total</th>
                            <th className="py-1.5 text-right font-medium">Input</th>
                            <th className="py-1.5 text-right font-medium">Output</th>
                          </tr>
                        </thead>
                        <tbody>
                          {users.map((userRow) => {
                            const userCalls = userRow.pluginCalls + userRow.skillCalls;
                            return (
                              <tr
                                key={userRow.user}
                                className="border-b border-border last:border-b-0"
                              >
                                <td className="py-2 text-[12px] font-medium text-ink">
                                  {userRow.user}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-2">
                                  {userRow.pluginCalls.toLocaleString()}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-2">
                                  {userRow.skillCalls.toLocaleString()}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-2">
                                  {avgTokens(userRow.totalTokens, userCalls)}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-2">
                                  {fmtTokens(userRow.totalTokens)}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-3">
                                  {fmtTokens(userRow.inputTokens)}
                                </td>
                                <td className="py-2 text-right text-[12px] text-ink-3">
                                  {fmtTokens(userRow.outputTokens)}
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

export { SkillsTable };
