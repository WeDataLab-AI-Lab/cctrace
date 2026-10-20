import type { AuditValue, TraceInstructionEntry } from '@/lib/session-trace-node-types';

const recordValue = (value: unknown): Record<string, unknown> | undefined =>
  value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined;

const readableContentValue = (value: unknown): unknown => {
  if (!Array.isArray(value) || value.length === 0) return value;
  const textBlockTypes = new Set(['text', 'input_text', 'output_text', 'summary_text']);
  const text: string[] = [];
  for (const item of value) {
    if (typeof item === 'string') { text.push(item); continue; }
    const block = recordValue(item);
    if (typeof block?.text === 'string' && (block.type === undefined || textBlockTypes.has(String(block.type)))) { text.push(block.text); continue; }
    return value;
  }
  return text.join('\n');
};

const auditBody = (value: unknown, fallbackState: string): AuditValue | undefined => {
  if (value === undefined || value === null) return;
  const object = recordValue(value);
  if (typeof object?.state === 'string') {
    return { ...object, value: readableContentValue(object.value) } as AuditValue;
  }
  return { state: fallbackState, value: readableContentValue(value) };
};

const extractTraceInstructions = (audit: AuditValue): TraceInstructionEntry[] => {
  const root = recordValue(audit.value);
  if (!root) return [];
  const typed = Array.isArray(root.instructions) ? root.instructions : undefined;
  if (typed) return typed.flatMap((item, index): TraceInstructionEntry[] => {
    const instruction = recordValue(item);
    const body = auditBody(instruction?.body, audit.state);
    if (!instruction || !body) return [];
    const kind = typeof instruction.kind === 'string' ? instruction.kind : 'recorded';
    const sourcePath = typeof instruction.source_path === 'string' ? instruction.source_path : `instructions[${index}]`;
    const path = typeof instruction.path === 'string' ? instruction.path : undefined;
    return [{ kind, source_path: sourcePath, ...(path === undefined ? {} : { path }), body }];
  });

  const attachment = recordValue(root.attachment);
  const payload = recordValue(root.payload) ?? root;
  const filesValue = attachment?.files ?? root.files;
  const files = Array.isArray(filesValue) ? filesValue.map(recordValue).filter((item): item is Record<string, unknown> => !!item) : [];
  const entries: TraceInstructionEntry[] = [];
  for (const file of files) {
    const body = auditBody(file.content ?? file.body, audit.state);
    if (!body) continue;
    entries.push({
      kind: 'file', source_path: attachment?.files === filesValue ? 'attachment.files' : 'files',
      path: typeof file.path === 'string' ? file.path : undefined, body,
    });
  }

  const instructionFields = [
    ['base_instructions', 'session'],
    ['developer_instructions', 'developer'],
    ['user_instructions', 'user'],
  ] as const;
  for (const [key, kind] of instructionFields) {
    const body = auditBody(payload[key], audit.state);
    if (body) entries.push({ kind, source_path: `payload.${key}`, body });
  }
  const collaboration = recordValue(payload.collaboration_mode);
  const settings = recordValue(collaboration?.settings);
  const collaborationBody = auditBody(settings?.developer_instructions, audit.state);
  if (collaborationBody) entries.push({ kind: 'developer', source_path: 'collaboration_mode.settings.developer_instructions', body: collaborationBody });
  if (entries.length > 0) return entries;

  const payloadMessage = recordValue(payload.message);
  const rootMessage = recordValue(root.message);
  const contentCandidates = [
    ['attachment.content', attachment?.content],
    ['content', root.content],
    ['payload.content', payload.content],
    ['payload.message.content', payloadMessage?.content],
    ['message.content', rootMessage?.content],
  ] as const;
  for (const [sourcePath, value] of contentCandidates) {
    const body = auditBody(value, audit.state);
    if (body) return [{ kind: 'recorded', source_path: sourcePath, body }];
  }
  return [];
};

export { extractTraceInstructions, readableContentValue };
