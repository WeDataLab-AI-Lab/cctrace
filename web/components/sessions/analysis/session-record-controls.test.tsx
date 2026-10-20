// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  fetchConsent: vi.fn(),
  grantConsent: vi.fn(),
  withdrawConsent: vi.fn(),
  deleteRecords: vi.fn(),
  user: { id: 1 },
}));

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>();
  return {
    ...actual,
    fetchSessionRecordConsent: mocks.fetchConsent,
    grantSessionRecordConsent: mocks.grantConsent,
    withdrawSessionRecordConsent: mocks.withdrawConsent,
    deleteOwnSessionRecords: mocks.deleteRecords,
  };
});
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: mocks.user }) }));

import { SessionRecordControls } from './session-record-controls';

const CONSENT = {
  runtime_key: 'claude-api:api_key', disclosure_version: '2026-10-07', granted: false, granted_at: null,
  runtime: 'claude-api', provider: 'Claude API', provider_retention: '확인되지 않음',
  disclosure: '세션 기록 생성을 위한 전송 안내', sends: ['사용자 입력'], not_sends: ['도구 출력'], enabled: true, runtime_available: true,
};

const mount = (ownerView = true) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(createElement(QueryClientProvider, { client }, createElement(SessionRecordControls, { ownerView })));
  return client;
};

afterEach(() => {
  cleanup();
  mocks.fetchConsent.mockReset();
  mocks.grantConsent.mockReset();
  mocks.withdrawConsent.mockReset();
  mocks.deleteRecords.mockReset();
});

describe('세션 기록 개인정보 제어', () => {
  it.each(['grant', 'withdraw'] as const)('%s 뒤 서버 동의 상태를 다시 읽고 다음 동작을 표시한다', async (action) => {
    const before = { ...CONSENT, granted: action === 'withdraw' };
    const after = { ...CONSENT, granted: action === 'grant' };
    mocks.fetchConsent.mockResolvedValueOnce(before).mockResolvedValue(after);
    mocks.grantConsent.mockResolvedValue(undefined);
    mocks.withdrawConsent.mockResolvedValue(undefined);
    mount();
    const user = userEvent.setup();
    if (action === 'grant') {
      await screen.findByText(/동의하지 않았습니다/);
      await user.click(screen.getByRole('button', { name: '동의 검토' }));
      await user.click(screen.getByRole('checkbox', { name: '고지 내용을 읽었고 전송에 동의합니다' }));
      await user.click(screen.getByRole('button', { name: '동의하고 저장' }));
      await screen.findByText('현재 런타임에 동의했습니다. · 공급자 Claude API');
      expect(screen.getByRole('button', { name: '동의 철회' })).toBeTruthy();
    } else {
      await screen.findByText(/동의했습니다/);
      await user.click(screen.getByRole('button', { name: '동의 철회' }));
      await user.click(await screen.findByRole('button', { name: '동의 철회 확인' }));
      await screen.findByText('현재 런타임에는 동의하지 않았습니다. · 공급자 Claude API');
      expect(screen.getByRole('button', { name: '동의 검토' })).toBeTruthy();
    }
    expect(mocks.fetchConsent).toHaveBeenCalledTimes(2);
  });

  it('비소유 세션에서는 동의·원문 파생 기록 제어를 요청하지 않는다', () => {
    mount(false);
    expect(screen.queryByText('세션 기록 전송 동의 및 내 기록 관리')).toBeNull();
    expect(mocks.fetchConsent).not.toHaveBeenCalled();
  });

  it('서버 고지를 확인하고 명시적으로 동의한 뒤에만 전송 동의를 저장한다', async () => {
    mocks.fetchConsent.mockResolvedValue(CONSENT);
    mocks.grantConsent.mockResolvedValue(undefined);
    mount();
    await screen.findByText(/동의하지 않았습니다/);
    await userEvent.setup().click(screen.getByRole('button', { name: '동의 검토' }));
    expect(await screen.findByText('세션 기록 생성을 위한 전송 안내')).toBeTruthy();
    expect(screen.getByText('사용자 입력')).toBeTruthy();
    const save = screen.getByRole('button', { name: '동의하고 저장' }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    await userEvent.setup().click(screen.getByRole('checkbox', { name: '고지 내용을 읽었고 전송에 동의합니다' }));
    expect(save.disabled).toBe(false);
    await userEvent.setup().click(save);
    expect(mocks.grantConsent).toHaveBeenCalledWith({ runtime_key: CONSENT.runtime_key, disclosure_version: CONSENT.disclosure_version });
  });

  it('동의 철회와 내 파생 기록 삭제를 별도 작업으로 제공한다', async () => {
    mocks.fetchConsent.mockResolvedValue({ ...CONSENT, granted: true, granted_at: '2026-10-08T12:00:00Z' });
    mocks.withdrawConsent.mockResolvedValue(undefined);
    mocks.deleteRecords.mockResolvedValue({ deleted: 3 });
    mount();
    await screen.findByText(/동의했습니다/);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '동의 철회' }));
    await user.click(await screen.findByRole('button', { name: '동의 철회 확인' }));
    expect(mocks.withdrawConsent).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole('button', { name: '내 기록 삭제' }));
    expect(await screen.findByText(/동의 철회와 별개/)).toBeTruthy();
    await user.click(screen.getByRole('button', { name: '내 기록 삭제 확인' }));
    expect(mocks.deleteRecords).toHaveBeenCalledTimes(1);
  });

  it('동의 API가 비활성 상태여도 내 기록 삭제 경로를 막지 않는다', async () => {
    mocks.fetchConsent.mockRejectedValue(new Error('records disabled'));
    mocks.deleteRecords.mockResolvedValue({ deleted: 0 });
    mount();
    expect(await screen.findByRole('alert')).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('button', { name: '내 기록 삭제' }));
    await userEvent.setup().click(await screen.findByRole('button', { name: '내 기록 삭제 확인' }));
    expect(mocks.deleteRecords).toHaveBeenCalledTimes(1);
  });

  it('기록 변경 뒤 C18 생성 상태를 실제 query key로 갱신한다', async () => {
    mocks.fetchConsent.mockResolvedValue({ ...CONSENT, granted: true });
    mocks.withdrawConsent.mockResolvedValue(undefined);
    const client = mount();
    await screen.findByText(/동의했습니다/);
    const invalidate = vi.spyOn(client, 'invalidateQueries');
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '동의 철회' }));
    await user.click(await screen.findByRole('button', { name: '동의 철회 확인' }));

    await waitFor(() => expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['session-record-status'] })));
  });
});
