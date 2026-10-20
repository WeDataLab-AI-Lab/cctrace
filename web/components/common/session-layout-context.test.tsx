// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, expect, it } from 'vitest';
import { SessionLayoutProvider, useSessionLayout } from './session-layout-context';

const Probe = () => {
  const layout = useSessionLayout();
  return <><output>{JSON.stringify([layout.menuCollapsed, layout.listCollapsed, layout.expanded])}</output>
    <button onClick={layout.toggleMenu}>menu</button><button onClick={layout.toggleList}>list</button>
    <button onClick={() => layout.setExpanded(true)}>expand</button><button onClick={() => layout.setExpanded(false)}>restore</button></>;
};
beforeEach(() => localStorage.clear());
afterEach(cleanup);
it('persists independent appearance preferences for each user without persisting expansion', () => {
  const view = render(<SessionLayoutProvider userId={7}><Probe /></SessionLayoutProvider>);
  fireEvent.click(screen.getByText('menu'));
  fireEvent.click(screen.getByText('expand'));
  fireEvent.click(screen.getByText('list'));
  fireEvent.click(screen.getByText('restore'));
  expect(screen.getByRole('status').textContent).toBe('[true,true,false]');
  view.unmount();
  const other = render(<SessionLayoutProvider userId={8}><Probe /></SessionLayoutProvider>);
  expect(screen.getByRole('status').textContent).toBe('[false,false,false]');
  other.unmount();
  render(<SessionLayoutProvider userId={7}><Probe /></SessionLayoutProvider>);
  expect(screen.getByRole('status').textContent).toBe('[true,true,false]');
});
