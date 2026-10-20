// @vitest-environment jsdom
import { mount, waitFor } from './page-test-fixture';
import { fireEvent, screen } from '@testing-library/react';
import { expect, it } from 'vitest';

it('캘린더는 목록 선택을 숨기고 직접 선택한 세션만 열며 닫으면 전체 폭을 복원한다', async () => {
  mount({ token_rule_pending: false, step: '', session_analysis: { enabled: true } }, false, true);
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('working-row'));
  const instance = screen.getByTestId('viewer-instance').textContent;
  fireEvent.click(screen.getByRole('button', { name: '세션 목록 접기' }));
  fireEvent.click(screen.getByRole('button', { name: '캘린더' }));
  await waitFor(() => expect(document.querySelector('[data-calendar-band]')).toBeTruthy());
  expect(screen.queryByRole('button', { name: '상세 영역 확대' })).toBeNull();
  expect(screen.getByTestId('viewer-enabled').textContent).toBe('false');
  expect(document.getElementById('session-navigation')?.hidden).toBe(false);
  fireEvent.click(document.querySelector('[data-calendar-band]')!);
  expect(screen.getByRole('button', { name: '대화창 닫기' })).toBeTruthy();
  expect(screen.getByTestId('viewer-enabled').textContent).toBe('true');
  fireEvent.click(screen.getByRole('button', { name: '상세 영역 확대' }));
  fireEvent.click(screen.getByRole('button', { name: '대화창 닫기' }));
  expect(screen.queryByRole('button', { name: '상세 영역 복원' })).toBeNull();
  expect(document.getElementById('session-navigation')?.hidden).toBe(false);
  expect(screen.getByTestId('viewer-enabled').textContent).toBe('false');
  fireEvent.click(screen.getByRole('button', { name: '목록' }));
  expect(screen.getByRole('button', { name: '상세 영역 확대' })).toBeTruthy();
  expect(screen.getByTestId('viewer-instance').textContent).toBe(instance);
  fireEvent.click(screen.getByRole('button', { name: '캘린더' }));
  expect(screen.queryByRole('button', { name: '대화창 닫기' })).toBeNull();
});
