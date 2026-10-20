import type { SessionRecord } from '@/lib/types';

const toolContextRecords = (): SessionRecord[] => [
  {
    ts: '2026-10-13T00:00:10Z', session_id: 'A', project_hash: 'fixture', profile_email: '', model: '', agent: 'codex', record_type: 'tool_call',
    raw: { private_fixture: 'raw fixture must stay hidden' },
    view: { kind: 'tool_call', role: 'assistant', anchor_id: 'call-record', dedupe_key: '',
      tool_calls: [{ id: 'call-1', name: 'Bash', input: 'printf trace-fixture' }] },
  },
  {
    ts: '2026-10-13T00:00:11Z', session_id: 'A', project_hash: 'fixture', profile_email: '', model: '', agent: 'codex', record_type: 'tool_result',
    raw: { private_fixture: 'raw result must stay hidden' },
    view: { kind: 'tool_result', anchor_id: 'result-record', dedupe_key: '',
      tool_results: [{ id: 'call-1', output: 'trace-fixture stdout', is_error: false }] },
  },
];

export { toolContextRecords };
