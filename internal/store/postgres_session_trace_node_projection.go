package store

// The list wire contains previews and relation keys only. In particular a list
// must not detoast/copy 100 complete tool outputs, instructions or blob bodies.
const traceNodeMetadataJSON = `jsonb_build_object(
 'ts',r.ts,'session_id',r.session_id,'uuid',r.uuid,'record_type',r.record_type,
 'profile_email',r.profile_email,'user_id',r.user_id,'login_email',r.login_email,'account_id',r.account_id,
 'agent',COALESCE(r.agent,'claude'),'source_file',r.source_file,'agent_id',r.agent_id,'is_sidechain',r.is_sidechain,
 'is_meta',r.is_meta,'prompt_source',r.prompt_source,'command_name',r.command_name,
 'tool_use_id',r.tool_use_id,'tool_name',r.tool_name,'tool_call_id',r.tool_call_id,'message_id',r.message_id,
 'codex_turn_id',r.codex_turn_id,'codex_root_turn_id',r.codex_root_turn_id,'codex_turn_metadata_known',r.codex_turn_metadata_known,
 'raw',jsonb_strip_nulls(jsonb_build_object(
  'type',r.raw->'type','id',r.raw->'id','requestRef',r.raw->'requestRef','subtype',r.raw->'subtype','toolUseID',r.raw->'toolUseID',
  'cctraceCollect',r.raw->'cctraceCollect','durationMs',r.raw->'durationMs','messageCount',r.raw->'messageCount',
  'compactMetadata',CASE WHEN jsonb_typeof(r.raw->'compactMetadata')='object' THEN jsonb_build_object(
    'trigger',r.raw->'compactMetadata'->'trigger','preTokens',r.raw->'compactMetadata'->'preTokens','postTokens',r.raw->'compactMetadata'->'postTokens') END,
  'role',r.raw->'role','name',r.raw->'name',
  'arguments',CASE WHEN jsonb_typeof(r.raw->'arguments')='object' THEN jsonb_build_object(
    'cmd',left(r.raw->'arguments'->>'cmd',96),'command',left(r.raw->'arguments'->>'command',96),'file_path',left(r.raw->'arguments'->>'file_path',96),
    'path',left(r.raw->'arguments'->>'path',96),'description',left(r.raw->'arguments'->>'description',96),'prompt',left(r.raw->'arguments'->>'prompt',96)) ELSE to_jsonb(left(r.raw->>'arguments',512)) END,
  'content',CASE WHEN jsonb_typeof(r.raw->'content')='string' THEN to_jsonb(left(r.raw->>'content',96))
    ELSE COALESCE((SELECT jsonb_agg(jsonb_build_object('type',b->'type','text',left(b->>'text',96)) ORDER BY ord)
     FROM jsonb_array_elements(CASE WHEN jsonb_typeof(r.raw->'content')='array' THEN r.raw->'content' ELSE '[]'::jsonb END) WITH ORDINALITY x(b,ord)),'[]'::jsonb) END,
  'cctraceHasAgentsInstructions',COALESCE(r.raw->'message'->>'content','') LIKE '%# AGENTS.md instructions%' OR COALESCE(r.raw->'payload'->>'content','') LIKE '%# AGENTS.md instructions%' OR COALESCE(r.raw->>'content','') LIKE '%# AGENTS.md instructions%',
  'cctraceInstructionKinds',(SELECT COALESCE(jsonb_agg(k),'[]'::jsonb) FROM (VALUES
    ('base_instructions',r.raw->'payload'->'base_instructions'),('developer_instructions',r.raw->'payload'->'developer_instructions'),
    ('user_instructions',r.raw->'payload'->'user_instructions'),('developer_instructions',r.raw->'payload'->'collaboration_mode'->'settings'->'developer_instructions')) v(k,value)
    WHERE value IS NOT NULL AND value<>'null'::jsonb AND value<>'""'::jsonb),
  'params',jsonb_build_object('model',r.raw->'params'->'model'),
  'redacted',r.raw->'redacted','withheld',r.raw->'withheld','cctraceWithheld',r.raw->'cctraceWithheld',
  'cctraceDropped',r.raw->'cctraceDropped','cctraceTruncated',r.raw->'cctraceTruncated',
  'attachment',jsonb_build_object('type',r.raw->'attachment'->'type','hookName',r.raw->'attachment'->'hookName','toolUseID',r.raw->'attachment'->'toolUseID',
    'path',left(COALESCE(r.raw->'attachment'->>'path',r.raw->'attachment'->'files'->0->>'path'),96)),
  'message',CASE WHEN jsonb_typeof(r.raw->'message')='object' THEN jsonb_build_object('id',r.raw->'message'->'id','role',r.raw->'message'->'role',
    'content',CASE WHEN jsonb_typeof(r.raw->'message'->'content')='string' THEN to_jsonb(left(r.raw->'message'->>'content',96))
     ELSE COALESCE((SELECT jsonb_agg(jsonb_build_object('type',b->'type','id',b->'id','name',b->'name','tool_use_id',b->'tool_use_id','is_error',b->'is_error',
      'text',CASE WHEN b->>'type' IN ('text','input_text','output_text') THEN left(b->>'text',96) END,
      'preview',left(COALESCE(b->'input'->>'file_path',b->'input'->>'path',b->'input'->>'cmd',b->'input'->>'command',b->'input'->>'description',b->'input'->>'prompt',''),96)) ORDER BY ord)
      FROM jsonb_array_elements(CASE WHEN jsonb_typeof(r.raw->'message'->'content')='array' THEN r.raw->'message'->'content' ELSE '[]'::jsonb END) WITH ORDINALITY x(b,ord)),'[]'::jsonb) END) END,
  'payload',CASE WHEN jsonb_typeof(r.raw->'payload')='object' THEN jsonb_build_object('type',r.raw->'payload'->'type','role',r.raw->'payload'->'role','name',r.raw->'payload'->'name',
    'model',left(r.raw->'payload'->>'model',96),'effort',left(r.raw->'payload'->>'effort',96),
    'turn_id',CASE WHEN jsonb_typeof(r.raw->'payload'->'turn_id')='string' THEN r.raw->'payload'->'turn_id' END,
    'root_turn_id',CASE WHEN jsonb_typeof(r.raw->'payload'->'root_turn_id')='string' THEN r.raw->'payload'->'root_turn_id' END,
    'internal_chat_message_metadata_passthrough',jsonb_build_object('turn_id',CASE WHEN jsonb_typeof(r.raw->'payload'->'internal_chat_message_metadata_passthrough'->'turn_id')='string' THEN r.raw->'payload'->'internal_chat_message_metadata_passthrough'->'turn_id' END,
      'content_item_kinds',CASE WHEN jsonb_typeof(r.raw->'payload'->'internal_chat_message_metadata_passthrough'->'content_item_kinds')='array' THEN r.raw->'payload'->'internal_chat_message_metadata_passthrough'->'content_item_kinds' END),
    'arguments',CASE WHEN jsonb_typeof(r.raw->'payload'->'arguments')='object' THEN jsonb_build_object(
      'cmd',left(r.raw->'payload'->'arguments'->>'cmd',96),'command',left(r.raw->'payload'->'arguments'->>'command',96),
      'file_path',left(r.raw->'payload'->'arguments'->>'file_path',96),'path',left(r.raw->'payload'->'arguments'->>'path',96),
      'description',left(r.raw->'payload'->'arguments'->>'description',96),'prompt',left(r.raw->'payload'->'arguments'->>'prompt',96))
      ELSE to_jsonb(left(r.raw->'payload'->>'arguments',512)) END,'input',left(r.raw->'payload'->>'input',96),
    'content',CASE WHEN jsonb_typeof(r.raw->'payload'->'content')='string' THEN to_jsonb(left(r.raw->'payload'->>'content',96))
     ELSE COALESCE((SELECT jsonb_agg(jsonb_build_object('type',b->'type','text',left(b->>'text',96)) ORDER BY ord)
      FROM jsonb_array_elements(CASE WHEN jsonb_typeof(r.raw->'payload'->'content')='array' THEN r.raw->'payload'->'content' ELSE '[]'::jsonb END) WITH ORDINALITY x(b,ord)),'[]'::jsonb) END) END
 )))`
