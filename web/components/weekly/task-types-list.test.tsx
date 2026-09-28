import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { buildTaskTypeView, TaskTypesList } from './task-types-list';

const tasks = [
  { task_type: 'non-task', prompt_count: 500 },
  { task_type: 'implementation', prompt_count: 300 },
  { task_type: 'unknown', prompt_count: 200 },
  { task_type: 'debugging', prompt_count: 100 },
];

const noop = () => {};

const render = (props: Partial<Parameters<typeof TaskTypesList>[0]> = {}) =>
  renderToStaticMarkup(
    createElement(TaskTypesList, {
      tasks,
      typedTurnCount: 1200,
      readState: 'read',
      onOpenTaskType: noop,
      ...props,
    }),
  );

describe('buildTaskTypeView', () => {
  // Shares are over requests the rules placed or have not reached yet. "non-task"
  // was never a request, and "unknown" waits for AI classification -- left in the
  // denominator it became the largest row and shrank every real one.
  it('takes shares over typed turns minus non-task and unknown', () => {
    const view = buildTaskTypeView(tasks, 1200);

    expect(view.denominator).toBe(1200 - 500 - 200);
    expect(view.rows).toEqual([
      { taskType: 'implementation', count: 300, share: '60%' },
      { taskType: 'debugging', count: 100, share: '20%' },
    ]);
  });

  it('reports unknown, unclassified and non-task as counts beside the rows', () => {
    const view = buildTaskTypeView(tasks, 1200);

    expect(view.unplaced).toBe(200);
    expect(view.nonTask).toBe(500);
    expect(view.unclassified).toBe(1200 - 1100);
    expect(view.unclassifiedShare).toBe('20%');
  });

  it('never reports a negative gap or divides by zero', () => {
    const view = buildTaskTypeView([{ task_type: 'unknown', prompt_count: 5 }], 3);

    expect(view.unclassified).toBe(0);
    expect(view.denominator).toBe(0);
  });
});

describe('TaskTypesList', () => {
  it('renders unknown as a line under the list, not as a share row', () => {
    const html = render();

    expect(html).toContain('규칙으로 분류하지 못한 요청 200건 · AI 분석 시 분류됨');
    expect(html).not.toContain('Request of no listed type');
    expect(html).toContain('60%');
  });

  it('keeps the "Not yet classified" and "Not a request" lines', () => {
    const html = render();

    expect(html).toContain('Not yet classified');
    expect(html).toContain('Not a request');
    expect(html).toContain('500 turns');
  });

  it('makes each task type row a button', () => {
    const html = render();

    expect(html.match(/<button/g)).toHaveLength(2);
  });

  it('draws no list while loading and says so when the read failed', () => {
    const loading = render({ readState: 'loading' });
    expect(loading).not.toContain('implementation');
    expect(loading).not.toContain('No prompts');

    expect(render({ readState: 'failed' })).toContain('불러오지 못했습니다');
  });

  it('shows one line for a week without prompts', () => {
    expect(render({ tasks: [], typedTurnCount: 0 })).toContain('이 기간에 기록된 요청이 없습니다.');
  });

  it('uses no card surface or shadow', () => {
    const html = render();

    expect(html).not.toMatch(/(?<![:\w-])bg-surface(?![-\w])/);
    expect(html).not.toContain('shadow');
  });
});
