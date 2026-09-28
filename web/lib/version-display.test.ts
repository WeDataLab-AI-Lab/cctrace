import { describe, expect, it } from 'vitest';
import { formatCctraceVersion, formatClaudeCodeVersion } from './version-display';

describe('formatCctraceVersion', () => {
  it('formats a reported version with a v prefix', () => {
    expect(formatCctraceVersion('0.7.6', 'v0.7.8')).toBe('v0.7.6');
    expect(formatCctraceVersion('v0.7.6', 'v0.7.8')).toBe('v0.7.6');
  });

  // The point of this module: a blank cctrace_version is a confirmed upper
  // bound (the column didn't exist yet), not an unknown value.
  it('formats a missing value as an upper bound below the introducing release', () => {
    expect(formatCctraceVersion(undefined, 'v0.7.8')).toBe('v0.7.8 미만');
    expect(formatCctraceVersion('', 'v0.7.8')).toBe('v0.7.8 미만');
  });
});

describe('formatClaudeCodeVersion', () => {
  it('formats a reported version verbatim', () => {
    expect(formatClaudeCodeVersion('1.2.3')).toBe('1.2.3');
  });

  // The asymmetry: Claude Code has no confirmed upper bound, so a missing
  // value must render as genuinely unknown, never as "< vX".
  it('formats a missing value as unknown, never as an upper bound', () => {
    expect(formatClaudeCodeVersion(undefined)).toBe('—');
    expect(formatClaudeCodeVersion('')).toBe('—');
  });
});
