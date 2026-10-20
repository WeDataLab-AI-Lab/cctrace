// @vitest-environment jsdom
import { createElement } from 'react';
import { cleanup, fireEvent, render } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import type { SessionTraceIndexEntry } from '@/lib/session-trace-index';
import { TimelineNavigator } from './timeline-navigator';

afterEach(cleanup);
// jsdom has no pointer capture; the navigator only needs these to survive a click.
Element.prototype.setPointerCapture = vi.fn();
Element.prototype.releasePointerCapture = vi.fn();

const turn = (no: number, start: string): SessionTraceIndexEntry => ({
  request_no: no, turn_key: `turn-${no}`, started_at: start, ended_at: new Date(Date.parse(start) + 60_000).toISOString(),
  preview: `요청 ${no}`, preview_state: 'ok', status: 'ok', api_requests: 1, tools: 0, subagents: 0, abandoned: false,
});

it('seeks to a whole-session request even when no conversation records are loaded', () => {
  const onJumpRequest = vi.fn();
  const onSelectRange = vi.fn();
  render(createElement(TimelineNavigator, {
    nodes: [], onJump: vi.fn(), onSelectRange, selectedRange: null,
    requestMarks: [turn(1, '2026-10-20T00:00:00Z'), turn(2, '2026-10-20T01:00:00Z')], onJumpRequest,
  }));
  const track = document.querySelector('[title^="옅은 영역"]');
  expect(track).toBeTruthy();
  Object.defineProperties(track!, { getBoundingClientRect: { value: () => ({ left: 0, width: 1000, top: 0, height: 36, right: 1000, bottom: 36 }) } });
  // Idle time between turns is collapsed, so the second turn sits near the right end.
  fireEvent.pointerDown(track!, { button: 0, pointerId: 1, clientX: 900 });
  fireEvent.pointerUp(track!, { button: 0, pointerId: 1, clientX: 900 });
  expect(onJumpRequest).toHaveBeenCalledWith('turn-2');
});

it('draws every request of the session before its pages load and opens an unloaded one in place', () => {
  const onOpenAt = vi.fn();
  const onJump = vi.fn();
  const at = (minute: number) => Date.parse('2026-10-20T00:00:00Z') + minute * 60_000;
  const requests = [0, 30, 60, 90].map(minute => ({ ts: new Date(at(minute)).toISOString(), ms: at(minute) }));
  render(createElement(TimelineNavigator, {
    nodes: [], onJump, onSelectRange: vi.fn(), selectedRange: null,
    timeline: { requests, replies: requests.map(request => request.ms + 5_000), spans: requests.map(request => ({ startMs: request.ms + 1_000, endMs: request.ms + 4_000, aside: false, error: false })) },
    // Only the newest request is loaded.
    loadedRange: { startMs: at(90), endMs: at(91) },
    onOpenAt,
  }));
  expect(document.querySelectorAll('.bg-brand.h-3\\.5')).toHaveLength(4);
  const track = document.querySelector('[title^="옅은 영역"]');
  Object.defineProperties(track!, { getBoundingClientRect: { value: () => ({ left: 0, width: 1000, top: 0, height: 36, right: 1000, bottom: 36 }) } });
  // Each request is one equal active stretch, so the second one starts near a quarter.
  fireEvent.pointerDown(track!, { button: 0, pointerId: 1, clientX: 300 });
  fireEvent.pointerUp(track!, { button: 0, pointerId: 1, clientX: 300 });
  expect(onOpenAt).toHaveBeenCalledWith(requests[1].ts);
  expect(onJump).not.toHaveBeenCalled();
});


it('draws a message typed inside a request as a small unnumbered mark', () => {
  const at = (minute: number) => Date.parse('2026-10-20T00:00:00Z') + minute * 60_000;
  const requests = [0, 30].map(minute => ({ ts: new Date(at(minute)).toISOString(), ms: at(minute) }));
  render(createElement(TimelineNavigator, {
    nodes: [], onJump: vi.fn(), onSelectRange: vi.fn(), selectedRange: null,
    timeline: { requests, replies: [], spans: [] },
    extraMessages: [at(10)],
    onOpenAt: vi.fn(),
  }));
  expect(document.querySelectorAll('.bg-brand.h-3\\.5')).toHaveLength(2);
  expect(document.querySelectorAll('.bg-brand\\/40.h-2')).toHaveLength(1);
});

