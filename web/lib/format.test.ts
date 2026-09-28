import { describe, expect, it } from 'vitest';
import { formatBytes, formatCompactNumber, formatCompressionDays, formatRelativeTime, formatRetentionDays } from './format';

describe('formatBytes', () => {
  it('formats across unit boundaries', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1024)).toBe('1.0 KB');
    expect(formatBytes(1024 * 1024)).toBe('1.0 MB');
    expect(formatBytes(1024 * 1024 * 1024)).toBe('1.0 GB');
    expect(formatBytes(3 * 1024 ** 4)).toBe('3.0 TB');
  });

  it('caps at TB for very large values', () => {
    expect(formatBytes(5000 * 1024 ** 4)).toBe('5000.0 TB');
  });

  it('renders dash for invalid input', () => {
    expect(formatBytes(-1)).toBe('—');
    expect(formatBytes(NaN)).toBe('—');
    expect(formatBytes(Infinity)).toBe('—');
  });
});

describe('formatRetentionDays', () => {
  it('shows day count, or Unlimited for no policy', () => {
    expect(formatRetentionDays(90)).toBe('90d');
    expect(formatRetentionDays(0)).toBe('0d');
    expect(formatRetentionDays(null)).toBe('Unlimited');
    expect(formatRetentionDays(undefined)).toBe('Unlimited');
  });
});

describe('formatCompressionDays', () => {
  it('shows day count, or None for no compression (not "Unlimited")', () => {
    expect(formatCompressionDays(30)).toBe('30d');
    expect(formatCompressionDays(0)).toBe('0d');
    expect(formatCompressionDays(null)).toBe('None');
    expect(formatCompressionDays(undefined)).toBe('None');
  });
});

describe('formatRelativeTime', () => {
  it('formats the shared minute, hour, and day boundaries', () => {
    const now = Date.now();
    expect(formatRelativeTime(new Date(now - 30_000).toISOString(), now)).toBe('Just now');
    expect(formatRelativeTime(new Date(now - 59 * 60_000).toISOString(), now)).toBe('59m ago');
    expect(formatRelativeTime(new Date(now - 23 * 3_600_000).toISOString(), now)).toBe('23h ago');
    expect(formatRelativeTime(new Date(now - 3 * 86_400_000).toISOString(), now)).toBe('3d ago');
  });
});

describe('formatCompactNumber', () => {
  it('shortens a fitted constant to the order of magnitude a reader is scanning for', () => {
    expect(formatCompactNumber(96365426)).toBe('96.4M');
    expect(formatCompactNumber(500)).toBe('500');
    expect(formatCompactNumber(1500)).toBe('1.5K');
  });
});
