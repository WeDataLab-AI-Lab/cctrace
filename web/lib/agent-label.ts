const AGENT_LABELS: Record<string, string> = {
  claude: 'Claude Code',
  codex: 'Codex',
};

const agentLabel = (agent: string): string => AGENT_LABELS[agent] ?? agent;

export { agentLabel };
