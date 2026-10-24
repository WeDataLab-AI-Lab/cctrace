// @vitest-environment jsdom
import { fireEvent, render, screen } from '@testing-library/react';
import { expect, it } from 'vitest';
import { ModelPieChart } from './model-pie-chart';

it('opens the Others legend details by button for touch and keyboard users', () => {
  render(<ModelPieChart viewMode="cost" data={[{ name: 'Others', value: 3 }]} otherNames={['sample-a', 'sample-b']} />);
  const button = screen.getByRole('button', { name: 'Others 상세' });
  expect(button.getAttribute('aria-expanded')).toBe('false');
  fireEvent.click(button);
  expect(button.getAttribute('aria-expanded')).toBe('true');
  expect(screen.getByText('sample-a, sample-b')).toBeTruthy();
});
