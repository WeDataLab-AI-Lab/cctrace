import { createElement, type ComponentType, type ReactNode } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { modelColor } from '@/lib/colors';
import type { UserStackDatum } from './overview-helpers';
import { UserStackChart } from './user-stack-chart';

type ViewMode = 'cost' | 'token';
type CapturedProps = Record<string, unknown>;

interface TestTooltipItem {
  name?: string | number;
  dataKey?: string | number;
  value?: number | string | ReadonlyArray<number | string>;
}

interface TestTooltipProps {
  active?: boolean;
  payload?: readonly TestTooltipItem[];
  viewMode: ViewMode;
}

const rechartsCapture = vi.hoisted(() => ({
  charts: [] as CapturedProps[],
  stacks: [] as CapturedProps[],
  bars: [] as CapturedProps[],
  tooltips: [] as CapturedProps[],
  legends: [] as CapturedProps[],
  rectangleCalls: 0,
}));

vi.mock('recharts', () => {
  const renderChildren = ({ children }: { children?: ReactNode }) => createElement('g', null, children);

  const ResponsiveContainer = renderChildren;
  const CartesianGrid = () => null;
  const XAxis = () => null;
  const YAxis = () => null;

  const BarChart = ({
    children,
    data,
    ...props
  }: {
    children?: ReactNode;
    data?: unknown;
    [key: string]: unknown;
  }) => {
    rechartsCapture.charts.push({ data, ...props });
    return createElement('svg', { 'data-testid': 'bar-chart' }, children);
  };

  const BarStack = ({ children, ...props }: { children?: ReactNode; [key: string]: unknown }) => {
    rechartsCapture.stacks.push(props);
    return createElement('g', { 'data-testid': 'bar-stack' }, children);
  };

  const Bar = (props: CapturedProps) => {
    rechartsCapture.bars.push(props);
    return createElement('rect', {
      'data-testid': 'bar',
      'data-key': String(props.dataKey),
    });
  };

  const Tooltip = (props: CapturedProps) => {
    rechartsCapture.tooltips.push(props);
    return null;
  };

  const Legend = ({ content, ...props }: { content?: unknown; [key: string]: unknown }) => {
    rechartsCapture.legends.push({ content, ...props });
    if (typeof content === 'function') return (content as () => ReactNode)();
    return (content as ReactNode) ?? null;
  };

  const Rectangle = () => {
    rechartsCapture.rectangleCalls += 1;
    return createElement('rect');
  };

  return {
    BarChart,
    Bar,
    BarStack,
    XAxis,
    YAxis,
    CartesianGrid,
    Tooltip,
    Legend,
    ResponsiveContainer,
    Rectangle,
  };
});

const tinyCostData: UserStackDatum[] = [
  { name: 'alice', 'gpt-5.5': 99.99, 'qwen-3': 0.01 },
];
const tinyTokenData: UserStackDatum[] = [
  { name: 'alice', 'gpt-5.5': 9999, 'qwen-3': 1 },
];
const multipleData: UserStackDatum[] = [
  { name: 'alice', 'gpt-5.5': 60, 'gpt-5.6-luna': 30, 'qwen-3': 10 },
  { name: 'bob', 'gpt-5.5': 25, 'gpt-5.6-luna': 75, 'qwen-3': 0 },
];
const modelColorData: UserStackDatum[] = [
  { name: 'alice', 'gpt-5.6-sol': 2, 'gpt-5.5': 3, Others: 5 },
];

const reset_recharts_capture = (): void => {
  rechartsCapture.charts.length = 0;
  rechartsCapture.stacks.length = 0;
  rechartsCapture.bars.length = 0;
  rechartsCapture.tooltips.length = 0;
  rechartsCapture.legends.length = 0;
  rechartsCapture.rectangleCalls = 0;
};

const render_chart = (viewMode: ViewMode, data: UserStackDatum[], stackKeys: string[], isLoading = false): string =>
  renderToStaticMarkup(createElement(UserStackChart, {
    viewMode,
    data,
    stackKeys,
    isLoading,
  }));

const assert_stack_contract = (): void => {
  expect(rechartsCapture.stacks).toHaveLength(1);
  expect(rechartsCapture.stacks[0]?.stackId).toBe('a');
  expect(rechartsCapture.stacks[0]?.radius).toEqual([0, 3, 3, 0]);
};

const assert_no_per_bar_geometry = (): void => {
  expect(rechartsCapture.rectangleCalls).toBe(0);
  for (const bar of rechartsCapture.bars) {
    expect(bar).not.toHaveProperty('radius');
    expect(bar).not.toHaveProperty('shape');
    expect(bar).not.toHaveProperty('minPointSize');
  }
};

const assert_bar_keys_and_stack = (stackKeys: string[]): void => {
  expect(rechartsCapture.bars.map((bar) => bar.dataKey)).toEqual(stackKeys);
  expect(rechartsCapture.bars.map((bar) => bar.stackId)).toEqual(stackKeys.map(() => 'a'));
};

const assert_chart_data = (data: UserStackDatum[]): void => {
  expect(rechartsCapture.charts).toHaveLength(1);
  expect(rechartsCapture.charts[0]?.data).toEqual(data);
};

const render_captured_tooltip = (viewMode: ViewMode): string => {
  const capturedContent = rechartsCapture.tooltips[0]?.content;
  if (!capturedContent || typeof capturedContent !== 'object') return '';

  const contentElement = capturedContent as {
    type: ComponentType<TestTooltipProps>;
    props: TestTooltipProps;
  };
  return renderToStaticMarkup(createElement(contentElement.type, {
    ...contentElement.props,
    active: true,
    payload: [
      { name: 'gpt-5.6-sol', value: 2 },
      { name: 'gpt-5.5', value: 3 },
      { name: 'Others', value: 5 },
    ],
    viewMode,
  }));
};

describe('UserStackChart stack-level rounding', () => {
  beforeEach(() => {
    reset_recharts_capture();
  });

  const test_cost_tiny_tail_uses_full_stack_right_radius = (): void => {
    const html = render_chart('cost', tinyCostData, ['gpt-5.5', 'qwen-3']);

    assert_stack_contract();
    assert_bar_keys_and_stack(['gpt-5.5', 'qwen-3']);
    assert_no_per_bar_geometry();
    assert_chart_data(tinyCostData);
    expect(html).toContain('Cost by User');
  };

  it('[TC-381-001] keeps the cost tiny tail under one full stack cap', test_cost_tiny_tail_uses_full_stack_right_radius);

  const test_token_tiny_tail_keeps_same_full_stack_radius_contract = (): void => {
    const html = render_chart('token', tinyTokenData, ['gpt-5.5', 'qwen-3']);

    assert_stack_contract();
    assert_bar_keys_and_stack(['gpt-5.5', 'qwen-3']);
    assert_no_per_bar_geometry();
    assert_chart_data(tinyTokenData);
    expect(html).toContain('Tokens by User');
  };

  it('[TC-381-002] keeps the token tiny tail under one full stack cap', test_token_tiny_tail_keeps_same_full_stack_radius_contract);

  const test_single_model_stack_keeps_flat_left_and_right_cap = (): void => {
    render_chart('cost', [{ name: 'alice', 'gpt-5.5': 100 }], ['gpt-5.5']);

    assert_stack_contract();
    assert_bar_keys_and_stack(['gpt-5.5']);
    assert_no_per_bar_geometry();
  };

  it('[TC-381-003] keeps a single model in the stack-level cap', test_single_model_stack_keeps_flat_left_and_right_cap);

  const test_multiple_user_rows_share_one_stack_without_internal_rounding = (): void => {
    render_chart('cost', multipleData, ['gpt-5.5', 'gpt-5.6-luna', 'qwen-3']);

    assert_stack_contract();
    assert_bar_keys_and_stack(['gpt-5.5', 'gpt-5.6-luna', 'qwen-3']);
    assert_no_per_bar_geometry();
    assert_chart_data(multipleData);
  };

  it('[TC-381-004] keeps every model Bar in one stack', test_multiple_user_rows_share_one_stack_without_internal_rounding);

  const test_zero_trailing_model_does_not_receive_rounding_or_visual_width = (): void => {
    render_chart('cost', multipleData, ['gpt-5.5', 'gpt-5.6-luna', 'qwen-3']);

    assert_stack_contract();
    assert_no_per_bar_geometry();
    assert_chart_data(multipleData);
    expect(multipleData[1]?.['qwen-3']).toBe(0);
  };

  it('[TC-381-005] preserves a zero trailing model without geometry props', test_zero_trailing_model_does_not_receive_rounding_or_visual_width);

  const test_tiny_tail_accepts_preceding_model_color_at_shared_cap = (): void => {
    render_chart('cost', tinyCostData, ['gpt-5.5', 'qwen-3']);

    assert_stack_contract();
    assert_no_per_bar_geometry();
    expect(rechartsCapture.bars.map((bar) => bar.fill)).toEqual([
      modelColor('gpt-5.5'),
      modelColor('qwen-3'),
    ]);
  };

  it('[TC-381-006] keeps real model colors available to the shared cap', test_tiny_tail_accepts_preceding_model_color_at_shared_cap);

  const test_stack_preserves_model_values_and_keys_without_min_point_size = (): void => {
    render_chart('cost', tinyCostData, ['gpt-5.5', 'qwen-3']);
    assert_chart_data(tinyCostData);
    assert_bar_keys_and_stack(['gpt-5.5', 'qwen-3']);
    assert_no_per_bar_geometry();

    reset_recharts_capture();
    render_chart('token', tinyTokenData, ['gpt-5.5', 'qwen-3']);
    assert_chart_data(tinyTokenData);
    assert_bar_keys_and_stack(['gpt-5.5', 'qwen-3']);
    assert_no_per_bar_geometry();
  };

  it('[TC-381-007] preserves cost and token values without a minimum point size', test_stack_preserves_model_values_and_keys_without_min_point_size);

  const test_loading_state_renders_skeleton_without_stack = (): void => {
    const html = render_chart('cost', tinyCostData, ['gpt-5.5', 'qwen-3'], true);

    expect(html).toContain('aria-busy="true"');
    expect(html).toContain('h-[280px]');
    expect(rechartsCapture.stacks).toHaveLength(0);
    expect(rechartsCapture.bars).toHaveLength(0);
    expect(rechartsCapture.charts).toHaveLength(0);
  };

  it('[TC-381-008a] renders loading without a stack', test_loading_state_renders_skeleton_without_stack);

  const test_empty_state_renders_no_data_without_stack = (): void => {
    const html = render_chart('cost', [], []);

    expect(html).toContain('No data');
    expect(rechartsCapture.stacks).toHaveLength(0);
    expect(rechartsCapture.bars).toHaveLength(0);
    expect(rechartsCapture.charts).toHaveLength(0);
  };

  it('[TC-381-008b] renders empty state without a stack', test_empty_state_renders_no_data_without_stack);

  const test_legend_and_tooltip_keep_model_colors_and_view_format = (): void => {
    const costHtml = render_chart('cost', modelColorData, ['gpt-5.6-sol', 'gpt-5.5', 'Others']);

    expect(costHtml).toContain('gpt-5.6-sol');
    expect(costHtml).toContain('gpt-5.5');
    expect(costHtml).toContain('Others');
    expect(rechartsCapture.bars.map((bar) => bar.fill)).toEqual([
      modelColor('gpt-5.6-sol'),
      modelColor('gpt-5.5'),
      modelColor('Others'),
    ]);
    expect(rechartsCapture.legends).toHaveLength(1);
    const costTooltip = render_captured_tooltip('cost');
    expect(costTooltip).toContain('gpt-5.6-sol : $2.00');
    expect(costTooltip).toContain('gpt-5.5 : $3.00');
    expect(costTooltip).toContain('Others : $5.00');

    reset_recharts_capture();
    render_chart('token', modelColorData, ['gpt-5.6-sol', 'gpt-5.5', 'Others']);
    const tokenTooltip = render_captured_tooltip('token');
    expect(tokenTooltip).toContain('gpt-5.6-sol : 2');
    expect(tokenTooltip).toContain('gpt-5.5 : 3');
    expect(tokenTooltip).toContain('Others : 5');
  };

  it('[TC-381-009] preserves model colors and view-specific tooltip format', test_legend_and_tooltip_keep_model_colors_and_view_format);
});
