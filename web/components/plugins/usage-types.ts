export interface CombinedUsage {
  name: string;
  pluginCalls: number;
  skillCalls: number;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  users: Set<string>;
}

export interface UserUsage {
  user: string;
  pluginCalls: number;
  skillCalls: number;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
}

export interface UsageGroup {
  key: string;
  label: string;
  items: CombinedUsage[];
  pluginCalls: number;
  skillCalls: number;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
}

export interface ByUserGroup {
  user: string;
  items: CombinedUsage[];
  pluginCalls: number;
  skillCalls: number;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
}

export type PluginView = 'skills' | 'users' | 'projects';

export interface Period {
  label: string;
  days: number;
}
