import type { SessionRecord } from './types';

interface TurnContextRequest { revision?: string; cursor?: string; limit?: number }
interface TurnContextResponse {
  session_id: string;
  turn_key: string;
  state: 'ready' | 'pending' | 'unavailable';
  revision: string;
  records: SessionRecord[];
  next_cursor: string | null;
}

export type { TurnContextRequest, TurnContextResponse };
