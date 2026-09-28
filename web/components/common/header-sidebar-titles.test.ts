import { describe, expect, it } from 'vitest';
import { WEEKLY_USAGE_HINT } from '@/lib/weekly-usage';
import { agentOptions, nonAnalyticsPaths, pageTitles } from './header';
import { navItems } from './sidebar';

// weekly report usage is its own chip, named as the charts name it, with the hover
// explanation that it is the server's runs rather than anyone's agent.
describe('agent scope chips', () => {
  it('offers weekly with its explanation', () => {
    const weekly = agentOptions.find((opt) => opt.value === 'weekly');

    expect(weekly?.label).toBe('weekly');
    expect(weekly?.hint).toBe(WEEKLY_USAGE_HINT);
  });
});

// header.tsx's pageTitles is a hand-maintained map keyed by route, separate from
// sidebar.tsx's navItems array. They drifted once already (#weekly report showing
// "Overview" as its title): a nav item was added without a matching title entry,
// and the header silently fell back to its '/' default instead of failing loudly.
// pageTitles legitimately has entries navItems doesn't (dead-route redirect stubs:
// /skills, /versions, /storage), so this only checks the direction that broke.
describe('sidebar navItems and header pageTitles', () => {
  it('gives every sidebar route a real page title', () => {
    const missing = navItems.map((item) => item.href).filter((href) => !(href in pageTitles));

    expect(missing).toEqual([]);
  });
});

// The scope chips are wired to every analytics route by default, so keeping one of
// them off the chips is a deliberate exclusion that nothing else enforces. /weekly is
// there because its scope axis is the person, not an account (#664,
// adr-weekly-retrospective.md decision 4). Dropping it from this set would put two
// filters back above a screen whose handler takes no scope parameter -- controls that
// change nothing, which is how they sat for months before anyone noticed.
describe('scope filter visibility', () => {
  it('keeps the Account/Agent chips off the weekly retrospective', () => {
    expect(nonAnalyticsPaths.has('/weekly')).toBe(true);
  });
});
