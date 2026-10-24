// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { useState } from 'react';
import type { ReactElement } from 'react';
import userEvent from '@testing-library/user-event';
import { SkillsTable } from './skills-table';
import { UsersTable } from './users-table';
import { DimensionUsageTable } from './dimension-usage-table';
import type { CombinedUsage } from './usage-types';

afterEach(cleanup);

const skill: CombinedUsage = {
  name: 'sample-skill', pluginCalls: 2, skillCalls: 3, totalTokens: 5,
  inputTokens: 3, outputTokens: 2, users: new Set(['test-user']),
};

it('lets a keyboard focusable control expand the skill row', () => {
  const onToggle = vi.fn();
  render(<SkillsTable rows={[skill]} bySkillUser={new Map()} expandedSkill={null} onToggle={onToggle} isLoading={false} />);
  const control = screen.getByRole('button', { name: 'sample-skill' });
  expect(control.getAttribute('aria-expanded')).toBe('false');
  fireEvent.click(control);
  expect(onToggle).toHaveBeenCalledWith('sample-skill');
});

it('lets a keyboard focusable control expand the user row', () => {
  const onToggle = vi.fn();
  render(<UsersTable byUser={[{ user: 'test-user', items: [skill], pluginCalls: 2, skillCalls: 3, totalTokens: 5, inputTokens: 3, outputTokens: 2 }]} expandedUser={null} onToggle={onToggle} isLoading={false} />);
  fireEvent.click(screen.getByRole('button', { name: 'test-user' }));
  expect(onToggle).toHaveBeenCalledWith('test-user');
});

it('lets a keyboard focusable control expand the project row', () => {
  const onToggle = vi.fn();
  render(<DimensionUsageTable title="Project" groups={[{ key: 'project-1', label: 'Sample project', items: [skill], pluginCalls: 2, skillCalls: 3, totalTokens: 5, inputTokens: 3, outputTokens: 2 }]} expanded={null} onToggle={onToggle} isLoading={false} />);
  fireEvent.click(screen.getByRole('button', { name: 'Sample project' }));
  expect(onToggle).toHaveBeenCalledWith('project-1');
});

const verifyRowInteraction = async (
  renderTable: (expanded: string | null, onToggle: (key: string) => void) => ReactElement,
  label: string,
  key: string,
) => {
  const user = userEvent.setup();
  const toggle = vi.fn();
  const ControlledTable = () => {
    const [expanded, setExpanded] = useState<string | null>(null);
    const onToggle = (next: string) => {
      toggle(next);
      setExpanded((current) => current === next ? null : next);
    };
    return renderTable(expanded, onToggle);
  };
  render(<ControlledTable />);
  const control = screen.getByRole('button', { name: label });
  const row = control.closest('tr')!;

  await user.click(row.cells[2]);
  expect(toggle).toHaveBeenCalledExactlyOnceWith(key);
  expect(control.getAttribute('aria-expanded')).toBe('true');

  await user.click(row.cells[0]);
  expect(toggle).toHaveBeenCalledTimes(2);
  expect(control.getAttribute('aria-expanded')).toBe('false');

  await user.click(control);
  expect(toggle).toHaveBeenCalledTimes(3);
  expect(control.getAttribute('aria-expanded')).toBe('true');
  await user.keyboard('{Enter}');
  expect(toggle).toHaveBeenCalledTimes(4);
  expect(control.getAttribute('aria-expanded')).toBe('false');
  await user.keyboard(' ');
  expect(toggle).toHaveBeenCalledTimes(5);
  expect(control.getAttribute('aria-expanded')).toBe('true');
};

it('toggles the skill row once from its count, arrow, and keyboard control', async () => {
  await verifyRowInteraction(
    (expandedSkill, onToggle) => <SkillsTable rows={[skill]} bySkillUser={new Map()} expandedSkill={expandedSkill} onToggle={onToggle} isLoading={false} />,
    'sample-skill', 'sample-skill',
  );
});

it('toggles the user row once from its count, arrow, and keyboard control', async () => {
  const byUser = [{ user: 'test-user', items: [skill], pluginCalls: 2, skillCalls: 3, totalTokens: 5, inputTokens: 3, outputTokens: 2 }];
  await verifyRowInteraction(
    (expandedUser, onToggle) => <UsersTable byUser={byUser} expandedUser={expandedUser} onToggle={onToggle} isLoading={false} />,
    'test-user', 'test-user',
  );
});

it('toggles the dimension row once from its count, arrow, and keyboard control', async () => {
  const groups = [{ key: 'project-1', label: 'Sample project', items: [skill], pluginCalls: 2, skillCalls: 3, totalTokens: 5, inputTokens: 3, outputTokens: 2 }];
  await verifyRowInteraction(
    (expanded, onToggle) => <DimensionUsageTable title="Project" groups={groups} expanded={expanded} onToggle={onToggle} isLoading={false} />,
    'Sample project', 'project-1',
  );
});
