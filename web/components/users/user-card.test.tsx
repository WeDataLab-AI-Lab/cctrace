// @vitest-environment jsdom
import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { CostSummary } from '@/lib/types';
import { UserCard } from './user-card';

afterEach(cleanup);

const longProfile = 'very.long.profile.name+suffix@subdomain.example.com';
const longLogin = 'another.long.login.address+team@subdomain.example.com';

const base = (over: Partial<CostSummary>): CostSummary => ({
  profile_email: 'a@example.com', user_id: 'user-a', user_team: '', model: 'claude-x', agent: 'claude',
  billing_provider: 'anthropic', total_cost: 1, total_input_tokens: 1, total_output_tokens: 1, request_count: 1,
  ...over,
}) as CostSummary;

const renderCard = (user: CostSummary) =>
  render(
    <UserCard
      user={user}
      avatarColor="#000"
      stats={user}
      viewMode="token"
      isAdmin={false}
      isSelected={false}
      isDragging={false}
      isDropTarget={false}
      isHovered={false}
      canAcceptDrop={false}
      onToggleSelect={vi.fn()}
      onHoverChange={vi.fn()}
      onDragStart={vi.fn()}
      onDragEnd={vi.fn()}
      onDragEnter={vi.fn()}
      onDragLeave={vi.fn()}
      onDrop={vi.fn()}
      onDelete={vi.fn()}
    />,
  );

describe('UserCard 이메일 줄', () => {
  it('말줄임되는 profile_email·login_emails 줄에 전체 주소를 title 로 단다', () => {
    const { getByText } = renderCard(base({ profile_email: longProfile, login_emails: [longLogin] }));
    expect(getByText(longProfile).getAttribute('title')).toBe(longProfile);
    expect(getByText(longLogin).getAttribute('title')).toBe(longLogin);
  });

  it('짧은 이메일도 기존 클래스(truncate 유지, 180px 열 불변)다', () => {
    const { getByText, container } = renderCard(base({}));
    expect(getByText('a@example.com').className).toBe('text-[11px] text-ink-2 truncate');
    expect(container.querySelector('.w-\\[180px\\].shrink-0')).not.toBeNull();
  });
});
