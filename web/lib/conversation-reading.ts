import type { ConversationReturnState } from './sessions-return-state';

interface ReadingPosition extends Omit<ConversationReturnState, 'sessionId' | 'mode'> { offset: number }
type RequestDirection = 'previous-request' | 'next-request';
interface ConversationDestination { direction?: RequestDirection; anchor?: string; sourceFile?: string; offset?: number; start?: 'latest-request'; fromTs?: string; turnKey?: string }

// Reading positions contain identifiers and presentation state only, never message bodies.
// Tab memory is scoped by authenticated identity, access filters, and session.
const positions = new Map<string, ReadingPosition>();
const conversationDestination = (anchor?: string, saved?: Pick<ReadingPosition, 'anchor' | 'offset' | 'readingAnchor'>): ConversationDestination =>
  anchor ? { anchor } : saved?.readingAnchor ? { anchor: saved.readingAnchor } : saved ? { offset: saved.offset } : { start: 'latest-request' };
const destinationReadingKey = (memoryKey: string, destination: ConversationDestination) => JSON.stringify([memoryKey, 'destination', destination]);
const rememberReading = (key: string, position: ReadingPosition) => { positions.set(key, position); };
const readReading = (key: string) => positions.get(key);
const clearReadingMemory = () => { positions.clear(); };

export { conversationDestination, destinationReadingKey, rememberReading, readReading, clearReadingMemory };
export type { ReadingPosition, ConversationDestination, RequestDirection };
