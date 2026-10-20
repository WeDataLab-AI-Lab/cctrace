import { describe, expect, it } from 'vitest';
import { inspectionEvents } from './session-inspection';
import type { SessionRecord } from './types';

const record = (raw: unknown, overrides: Partial<SessionRecord> = {}): SessionRecord => ({ ts:'2026-10-17T10:00:00Z',session_id:'s',project_hash:'p',record_type:'attachment',profile_email:'',model:'',uuid:'r',raw,...overrides });

describe('inspectionEvents preserves collected content and exact sources', () => {
 it('shows every injected file, including AGENTS.md, at its own content path', () => {
  const events=inspectionEvents(record({attachment:{type:'instructions',files:[{path:'CLAUDE.md',content:'claude'},{path:'AGENTS.md',content:'agents'}]}}));
  expect(events.map(e=>[e.kind,e.label,e.text,e.target.contentPath])).toEqual([
   ['instructions','CLAUDE.md','claude','/attachment/files/0'], ['instructions','AGENTS.md','agents','/attachment/files/1'],
  ]);
 });
 it('keeps each tool block and result source instead of joining away records', () => {
  const events=inspectionEvents(record({message:{role:'assistant',content:[{type:'text',text:'read'},{type:'tool_use',id:'c',name:'Read',input:{file:'x'}}]}},{record_type:'assistant'}));
  expect(events.map(e=>e.kind)).toEqual(['message','tool_call']);
  expect(events[1].callId).toBe('c');expect(events[1].target.contentPath).toBe('/message/content/1');
  const result=inspectionEvents(record({message:{role:'user',content:[{type:'tool_result',tool_use_id:'c',content:'done'}]}},{uuid:'result'}));
  expect(result[0].kind).toBe('tool_result');expect(result[0].target.recordId).toBe('result');
 });
 it('exposes developer, Codex metadata instructions, hooks and unknown records', () => {
  const developer=inspectionEvents(record({payload:{type:'message',role:'developer',content:[{type:'input_text',text:'policy'}]}},{agent:'codex'}));
  expect(developer[0].text).toBe('policy');expect(developer[0].kind).toBe('instructions');
  const context=inspectionEvents(record({payload:{base_instructions:{text:'base'},developer_instructions:'dev'}},{agent:'codex',record_type:'context'}));
  expect(context.map(e=>e.text)).toEqual(['base','dev']);
  const hook=inspectionEvents(record({attachment:{type:'hook_success',toolUseID:'c',stdout:'hook output',exitCode:0}}));
  expect(hook[0].kind).toBe('hook');expect(hook[0].callId).toBe('c');expect(hook[0].text).toContain('hook output');
  const unknown=inspectionEvents(record({new_type:{body:'must remain'}}));
  expect(unknown[0].kind).toBe('record');expect(unknown[0].text).toContain('must remain');
 });
 it('distinguishes redaction, withheld and truncation without claiming execution absence', () => {
  const events=inspectionEvents(record({attachment:{type:'instructions'},cctraceWithheld:['attachment.files'],cctraceTruncated:['attachment.content']}));
  expect(events[0].notices).toEqual(expect.arrayContaining(['본문 미수집: attachment.files','원자료 잘림: attachment.content']));
 });
 it('does not call recovered fields missing when historical collection markers remain', () => {
  const events=inspectionEvents(record({attachment:{type:'instructions',files:[{path:'AGENTS.md',content:'restored'}]},cctraceWithheld:['attachment.files'],cctraceDropped:['attachment.files']}));
  expect(events[0].text).toBe('restored');
  expect(events[0].notices).not.toContain('본문 미수집: attachment.files');
  expect(events[0].notices).not.toContain('수집에서 제외됨: attachment.files');
  expect(events[0].record.raw).toHaveProperty('cctraceWithheld');
  const redacted=inspectionEvents(record({message:{role:'user',content:'[redacted by cctrace client]'}},{record_type:'user'}));
  expect(redacted[0].notices).toContain('수집 시 가림 처리됨');
 });
});

it('reads top-level developer bodies and preserves their actual content pointer', () => {
 for (const raw of [
  {type:'message',role:'developer',content:[{type:'input_text',text:'top-level instructions'}]},
  {type:'message',content:[{type:'input_text',text:'top-level instructions'}]},
 ]) {
  const events=inspectionEvents(record(raw,{agent:'codex',record_type:'developer'}));
  expect(events[0].kind).toBe('instructions');
  expect(events[0].text).toBe('top-level instructions');
  expect(events[0].target.contentPath).toBe('/content');
 }
});
