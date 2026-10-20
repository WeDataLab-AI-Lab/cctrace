import { describe, expect, it } from 'vitest';
import { conversationDestination, rememberReading, readReading, clearReadingMemory } from './conversation-reading';

describe('conversation destination', () => {
  it('prioritizes explicit links, then the last read position, then the recent request', () => {
    expect(conversationDestination('exact', { anchor: 'saved', offset: 400 })).toEqual({ anchor: 'exact' });
    expect(conversationDestination(undefined, { anchor: 'saved', offset: 400 })).toEqual({ offset: 400 });
    expect(conversationDestination()).toEqual({ start: 'latest-request' });
  });
  it('keeps reading positions private to the user, access scope, and session', () => {
    clearReadingMemory();
    rememberReading('user-a:owner:A', { offset: 400, scrollTop: 32, visible: 60, promptsOnly: false });
    expect(readReading('user-b:owner:A')).toBeUndefined();
    expect(readReading('user-a:admin:A')).toBeUndefined();
    expect(readReading('user-a:owner:A')?.scrollTop).toBe(32);
    clearReadingMemory();
    expect(readReading('user-a:owner:A')).toBeUndefined();
  });
});
