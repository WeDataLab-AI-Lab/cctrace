import { agentOf } from '@/lib/colors';

const shortModel = (model: string): string =>
  model.replace('claude-', '').split('-2025')[0].split('-2024')[0];

const CLAUDE_VERSION_PATTERN = '\\d{1,2}(?:-\\d{1,2})*';

const claudeModelKey = (model: string): string => {
  const short = shortModel(model);
  const m = model.toLowerCase();
  const tier = ['fable', 'opus', 'sonnet', 'haiku'].find((name) => m.includes(name));
  if (!tier) return short;

  const normalizedShortModel = short.toLowerCase();
  const versionAfterTier = normalizedShortModel.match(new RegExp(`${tier}-(${CLAUDE_VERSION_PATTERN})(?:-|$)`))?.[1];
  const versionBeforeTier = normalizedShortModel.match(new RegExp(`(?:^|-)(${CLAUDE_VERSION_PATTERN})-${tier}(?:-|$)`))?.[1];
  const version = (versionAfterTier ?? versionBeforeTier)?.replaceAll('-', '.');
  return version ? `${tier} ${version}` : tier;
};

// 분류는 agentOf(colors.ts SSOT)에 위임: claude|codex|compat 3종.
// compat(qwen/glm/kimi 등)는 Others로 접지 않고 자기 키를 갖는다.
// 필터 드롭다운·트렌드 차트·cost-user-stack 차트가 모두 이 라벨을 SSOT로 공유한다.
const modelLabel = (model: string): string =>
  agentOf(model) === 'claude' ? claudeModelKey(model) : shortModel(model);

export { modelLabel, shortModel };
