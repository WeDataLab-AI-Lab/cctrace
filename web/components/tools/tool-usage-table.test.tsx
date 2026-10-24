// @vitest-environment jsdom
import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup } from '@testing-library/react';
import { useState } from 'react';
import userEvent from '@testing-library/user-event';
import { ToolUsageTable } from './tool-usage-table';

vi.mock('./tool-detail-panel', () => ({ ToolDetailPanel: () => <div>Tool details</div> }));

afterEach(cleanup);

it('opens and closes a tool detail from its keyboard focusable row control', () => {
  const toggle = vi.fn();
  const rows = [{ tool_name: 'Bash', use_count: 12, success_count: 10, fail_count: 2 }];
  const view = render(<ToolUsageTable rows={rows} isLoading={false} expandedTool={null} onToggleTool={toggle} />);
  const control = screen.getByRole('button', { name: /Bash/ });
  expect(control.getAttribute('aria-expanded')).toBe('false');
  fireEvent.click(control);
  expect(toggle).toHaveBeenCalledWith('Bash');
  view.rerender(<ToolUsageTable rows={rows} isLoading={false} expandedTool="Bash" onToggleTool={toggle} />);
  expect(screen.getByText('Tool details')).toBeTruthy();
  expect(screen.getByRole('button', { name: /Bash/ }).getAttribute('aria-expanded')).toBe('true');
});

it('toggles tool details once from the count, arrow, and keyboard control', async () => {
  const user = userEvent.setup();
  const toggle = vi.fn();
  const rows = [{ tool_name: 'Bash', use_count: 12, success_count: 10, fail_count: 2 }];
  const ControlledTable = () => {
    const [expanded, setExpanded] = useState<string | null>(null);
    const onToggle = (name: string | null) => {
      toggle(name);
      setExpanded(name);
    };
    return <ToolUsageTable rows={rows} isLoading={false} expandedTool={expanded} onToggleTool={onToggle} />;
  };
  render(<ControlledTable />);
  const control = screen.getByRole('button', { name: 'Bash' });
  const row = control.closest('tr')!;

  await user.click(row.cells[2]);
  expect(toggle).toHaveBeenCalledExactlyOnceWith('Bash');
  expect(control.getAttribute('aria-expanded')).toBe('true');
  expect(screen.getByText('Tool details')).toBeTruthy();

  await user.click(row.cells[0]);
  expect(toggle).toHaveBeenCalledTimes(2);
  expect(toggle).toHaveBeenLastCalledWith(null);
  expect(control.getAttribute('aria-expanded')).toBe('false');
  expect(screen.queryByText('Tool details')).toBeNull();

  await user.click(control);
  expect(toggle).toHaveBeenCalledTimes(3);
  expect(control.getAttribute('aria-expanded')).toBe('true');
  await user.keyboard('{Enter}');
  expect(toggle).toHaveBeenCalledTimes(4);
  expect(control.getAttribute('aria-expanded')).toBe('false');
  await user.keyboard(' ');
  expect(toggle).toHaveBeenCalledTimes(5);
  expect(control.getAttribute('aria-expanded')).toBe('true');
});
