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
import type { ToolUsageSummary } from '@/lib/types';
import { ToolRateBar } from './tool-rate-bar';
import { ToolDetailPanel } from './tool-detail-panel';

interface ToolUsageTableProps {
  rows: ToolUsageSummary[];
  isLoading: boolean;
  expandedTool: string | null;
  onToggleTool: (toolName: string | null) => void;
  loginEmail?: string;
}

const ToolUsageTable = ({
  rows,
  isLoading,
  expandedTool,
  onToggleTool,
  loginEmail,
}: ToolUsageTableProps) => (
  <div className="bg-surface rounded-lg border border-border overflow-hidden">
    <Table className="text-sm">
      <TableHeader className="bg-canvas text-ink-3 text-xs uppercase">
        <TableRow className="hover:bg-transparent">
          <TableHead className="px-4 py-3 text-left w-8 text-ink-3" />
          <TableHead className="px-4 py-3 text-left text-ink-3">Tool</TableHead>
          <TableHead className="px-4 py-3 text-right text-ink-3">Uses</TableHead>
          <TableHead className="px-4 py-3 text-right text-ink-3">Success</TableHead>
          <TableHead className="px-4 py-3 text-right text-ink-3">Failures</TableHead>
          <TableHead className="px-4 py-3 text-left w-48 text-ink-3">Rate</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody className="divide-y divide-border/50">
        {isLoading ? (
          <TableRow className="hover:bg-transparent">
            <TableCell colSpan={6} className="px-4 py-8 text-center text-ink-3">
              <CollectingLoader />
            </TableCell>
          </TableRow>
        ) : rows.length === 0 ? (
          <TableRow className="hover:bg-transparent">
            <TableCell colSpan={6} className="px-4 py-8 text-center text-ink-3">
              No data
            </TableCell>
          </TableRow>
        ) : (
          rows.map((t) => {
            const rate = t.use_count > 0 ? (t.success_count / t.use_count) * 100 : 0;
            const isExpanded = expandedTool === t.tool_name;
            const handleToggle = () => onToggleTool(isExpanded ? null : t.tool_name);

            return (
              <Fragment key={t.tool_name}>
                <TableRow
                  onClick={handleToggle}
                  className={cn(
                    'cursor-pointer transition-colors',
                    isExpanded ? 'bg-surface-sunk hover:bg-surface-sunk' : 'hover:bg-canvas',
                  )}
                >
                  <TableCell className="pl-4 py-3 text-ink-3">
                    {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                  </TableCell>
                  <TableCell className="px-4 py-3 font-medium text-ink">{t.tool_name}</TableCell>
                  <TableCell className="px-4 py-3 text-right text-ink-2">
                    {t.use_count.toLocaleString()}
                  </TableCell>
                  <TableCell className="px-4 py-3 text-right text-success">
                    {t.success_count.toLocaleString()}
                  </TableCell>
                  <TableCell className="px-4 py-3 text-right text-danger">
                    {t.fail_count.toLocaleString()}
                  </TableCell>
                  <TableCell className="px-4 py-3">
                    <ToolRateBar rate={rate} />
                  </TableCell>
                </TableRow>
                {isExpanded && (
                  <TableRow key={`${t.tool_name}-detail`} className="hover:bg-transparent">
                    <TableCell colSpan={6} className="p-0 bg-surface-sunk">
                      <ToolDetailPanel toolName={t.tool_name} loginEmail={loginEmail} />
                    </TableCell>
                  </TableRow>
                )}
              </Fragment>
            );
          })
        )}
      </TableBody>
    </Table>
  </div>
);

export { ToolUsageTable };
