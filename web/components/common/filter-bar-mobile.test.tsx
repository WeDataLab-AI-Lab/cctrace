// @vitest-environment jsdom
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { FilterBar } from './filter-bar';

it('keeps model choices in a keyboard-operable viewport-aware menu', async () => {
  const choose = vi.fn();
  render(<FilterBar showViewToggle={false} showModelCategory={false} showGroupBy={false} showRefresh={false}
    showModelFilter modelFilter="__all__" models={['sample']} onModelFilterChange={choose} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: 'All Models' }));
  expect(screen.getByRole('menu')).toBeTruthy();
  await user.click(screen.getByRole('menuitem', { name: 'sample' }));
  expect(choose).toHaveBeenCalledWith('sample');
});
