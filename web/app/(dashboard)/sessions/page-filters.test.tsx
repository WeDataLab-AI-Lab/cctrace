// @vitest-environment jsdom
import { mount, waitFor } from './page-test-fixture';
import { fireEvent, screen } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { fetchSessionOverview } from '@/lib/api';

it('추가 필터는 기본으로 접히고 펼쳐도 세션을 다시 요청하거나 상세를 재마운트하지 않는다', async () => {
  mount(undefined, false, true, false);
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('working-row'));
  expect(screen.getByRole('textbox', { name: '세션 검색' })).toBeTruthy();
  expect(screen.getByTestId('select-first-project').closest('[hidden]')).toBeNull();
  expect(screen.queryByRole('button', { name: '서버 세션' })).toBeNull();
  expect(screen.getByRole('button', { name: '추가 필터 펼치기' }).getAttribute('aria-expanded')).toBe('false');
  const instance = screen.getByTestId('viewer-instance').textContent;
  vi.mocked(fetchSessionOverview).mockClear();
  fireEvent.click(screen.getByRole('button', { name: '추가 필터 펼치기' }));
  expect(screen.getByRole('textbox', { name: '세션 검색' })).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: '추가 필터 접기' }));
  expect(screen.getByTestId('viewer-instance').textContent).toBe(instance);
  expect(fetchSessionOverview).not.toHaveBeenCalled();
});

it('검색 조건은 접힌 요약에 남고 상세 확대·복원 후 펼침 상태와 입력값을 유지한다', async () => {
  mount(undefined, false, true, false);
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('working-row'));
  const instance = screen.getByTestId('viewer-instance').textContent;
  fireEvent.click(screen.getByRole('button', { name: '추가 필터 펼치기' }));
  fireEvent.change(screen.getByRole('textbox', { name: '세션 검색' }), { target: { value: 'row' } });
  fireEvent.click(screen.getByRole('button', { name: '추가 필터 접기' }));
  expect(screen.getByLabelText('현재 세션 조회 조건').textContent).toContain('row');
  fireEvent.click(screen.getByRole('button', { name: '상세 영역 확대' }));
  expect(screen.queryByRole('button', { name: '추가 필터 펼치기' })).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: '상세 영역 복원' }));
  expect(screen.getByRole('button', { name: '추가 필터 펼치기' })).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: '추가 필터 펼치기' }));
  fireEvent.click(screen.getByRole('button', { name: '상세 영역 확대' }));
  fireEvent.click(screen.getByRole('button', { name: '상세 영역 복원' }));
  expect(screen.getByRole('textbox', { name: '세션 검색' })).toHaveProperty('value', 'row');
  expect(screen.getByTestId('viewer-instance').textContent).toBe(instance);
});


it('접힌 상태에서 목록·캘린더를 전환하고 프로젝트와 검색 입력을 유지한다', async () => {
  mount({ token_rule_pending: false, step: '', session_analysis: { enabled: true } }, false, true, false);
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('working-row'));
  fireEvent.change(screen.getByRole('textbox', { name: '세션 검색' }), { target: { value: 'row' } });
  fireEvent.click(screen.getByRole('button', { name: '캘린더' }));
  expect(screen.getByRole('button', { name: '캘린더' }).getAttribute('aria-pressed')).toBe('true');
  expect(screen.getByRole('textbox', { name: '세션 검색' })).toHaveProperty('value', 'row');
  expect(screen.getByTestId('select-first-project').closest('[hidden]')).toBeNull();
  expect(screen.getByRole('button', { name: '추가 필터 펼치기' }).getAttribute('aria-expanded')).toBe('false');
  fireEvent.click(screen.getByRole('button', { name: '목록' }));
  fireEvent.click(screen.getByRole('button', { name: '상세 영역 확대' }));
  expect(screen.queryByRole('button', { name: '캘린더' })).toBeNull();
  expect(screen.queryByRole('textbox', { name: '세션 검색' })).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: '상세 영역 복원' }));
  expect(screen.getByRole('textbox', { name: '세션 검색' })).toHaveProperty('value', 'row');
});
