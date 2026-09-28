const PROJECT_TREND_COLLAPSED_KEY = 'sessions:projectTrendCollapsed';

const projectTrendView = (collapsed: boolean) => ({
  chartVisible: !collapsed,
  toggleLabel: collapsed ? 'Expand cost trend' : 'Collapse cost trend',
});

export { PROJECT_TREND_COLLAPSED_KEY, projectTrendView };
