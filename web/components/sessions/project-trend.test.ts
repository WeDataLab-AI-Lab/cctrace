import { describe, expect, it } from 'vitest';
import { projectTrendView } from './project-trend';

describe('projectTrendView', () => {
  it('keeps the project cost trend visible by default', () => {
    expect(projectTrendView(false)).toEqual({
      chartVisible: true,
      toggleLabel: 'Collapse cost trend',
    });
  });

  it('hides the chart and exposes an expand action when collapsed', () => {
    expect(projectTrendView(true)).toEqual({
      chartVisible: false,
      toggleLabel: 'Expand cost trend',
    });
  });
});
