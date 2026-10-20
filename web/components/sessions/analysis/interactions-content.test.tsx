// @vitest-environment jsdom
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { A_ID, E_ID, P_ID } from '@/app/test/session-analysis/fixtures';
import { formatDateTime } from './format';
import { flagshipFull, passA, mount } from './analysis-interaction-test-fixture';

describe('복사 원본 열기', () => {
  it('원본 세션 id로 onOpenSession이 불린다 (패스 A의 E)', async () => {
    const onOpenSession = vi.fn<(id: string) => void>();
    mount(passA(E_ID), { onOpenSession });
    await userEvent.click(screen.getByRole('button', { name: /원본 세션 ad704e10 열기/ }));
    expect(onOpenSession).toHaveBeenCalledWith(A_ID);
  });
});


describe('플랜 본문', () => {
  it('합성 파일 갱신 시각은 ExitPlanMode 결정 시각과 별도로 표시한다', () => {
    const analysis = flagshipFull();
    const plan = analysis.plans[0];
    if (!plan?.file_updated_at) throw new Error('flagship plan must have file metadata');
    expect(plan.file_updated_at).not.toBe(plan.ts);
    mount(analysis, { timeZone: 'UTC' });
    const panel = within(screen.getByRole('region', { name: '세션 분석' })).getByRole('region', { name: '플랜' });
    const card = panel.querySelector<HTMLElement>(':scope > div > ul > li');
    if (!card) throw new Error('plan card missing');
    const decision = within(card).getByText('승인');
    const exitTime = within(card).getByText(formatDateTime(plan.ts, 'UTC'));
    const fileTime = within(card).getByText(`파일 갱신 ${formatDateTime(plan.file_updated_at, 'UTC')}`);
    expect(decision).toBeTruthy();
    expect(exitTime.closest('time')?.getAttribute('datetime')).toBe(plan.ts);
    expect(fileTime.closest('[title]')?.getAttribute('title')).toContain('승인·반려 시각과는 다릅니다');
    expect(exitTime.textContent).not.toBe(fileTime.textContent?.replace('파일 갱신 ', ''));
  });

  it('파일 시각을 수집하지 않은 플랜은 ExitPlanMode 시각을 파일 시각으로 추정하지 않는다', () => {
    const analysis = flagshipFull();
    const plan = analysis.plans[0];
    if (!plan) throw new Error('flagship plan missing');
    mount({ ...analysis, plans: [{ ...plan, file_updated_at: undefined }] });
    const panel = within(screen.getByRole('region', { name: '세션 분석' })).getByRole('region', { name: '플랜' });
    const card = panel.querySelector<HTMLElement>(':scope > div > ul > li');
    if (!card) throw new Error('plan card missing');
    expect(within(card).getByText('승인')).toBeTruthy();
    expect(within(card).getByText(formatDateTime(plan.ts)).closest('time')?.getAttribute('datetime')).toBe(plan.ts);
    expect(within(card).getByText('파일 갱신 시각 미수집')).toBeTruthy();
  });

  it('긴 플랜 본문은 접힌 동안 inert라 잘린 링크가 Tab 순서에 없다 (패스 A의 A)', async () => {
    mount(passA(A_ID));
    const toggle = screen.getAllByRole('button', { name: '전체 보기' })[0];
    const body = document.getElementById(toggle.getAttribute('aria-controls') ?? '');
    expect(body?.hasAttribute('inert')).toBe(true);
    await userEvent.click(toggle);
    expect(body?.hasAttribute('inert')).toBe(false);
    expect(document.activeElement).toBe(toggle);
  });
});


describe('상한에 걸린 긴 세션', () => {
  it('"더 보기"가 있다 (패스 A의 P)', () => {
    mount(passA(P_ID));
    expect(screen.getByRole('button', { name: '1,900개 더 보기' })).toBeTruthy();
  });
});
