// @vitest-environment jsdom
import { createElement } from 'react';
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { JudgementCountFilters } from './judgement-count-filters';

afterEach(cleanup);

describe('실제 목록 판정 bucket 필터', () => {
  it('서버가 관측한 0과 판정 불가 개수를 구별한다', () => {
    render(createElement(JudgementCountFilters, {
      counts: { done: 0, leftover: 2, ended_midway: 1, unknown: 4 }, value: null, onChange: vi.fn(),
    }));
    expect(screen.getByRole('button', { name: '완료 0' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '하다 만 것 2' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '도중 종료 1' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '판정 불가 4' })).toBeTruthy();
  });

  it('counts를 받지 못하면 숫자 대신 ?를 보이고 필터를 비활성화한다', () => {
    render(createElement(JudgementCountFilters, { value: null, onChange: vi.fn(), loading: true }));
    const button = screen.getByRole('button', { name: '완료 ?' }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(screen.getByRole('status').textContent).toContain('판정 개수를 불러오는 중');
  });

  it('버튼은 서버의 bucket 이름을 부모에게 보낸다', async () => {
    const onChange = vi.fn();
    render(createElement(JudgementCountFilters, {
      counts: { done: 0, leftover: 0, ended_midway: 0, unknown: 0 }, value: null, onChange,
    }));
    await userEvent.setup().click(screen.getByRole('button', { name: '도중 종료 0' }));
    expect(onChange).toHaveBeenCalledWith('ended_midway');
  });
});
