import { renderToStaticMarkup } from 'react-dom/server';
import { expect, it } from 'vitest';
import { FilterBar } from './filter-bar';

it('keeps the requested project picker visible while project options are empty', () => {
  const html = renderToStaticMarkup(<FilterBar showViewToggle={false} showModelCategory={false}
    showProjectFilter projects={[]} onProjectChange={() => {}} />);
  expect(html).toContain('All Projects');
  expect(html).toContain('aria-haspopup="menu"');
});

it('only shows the project picker when requested', () => {
  const html = renderToStaticMarkup(<FilterBar showProjectFilter={false} projects={[]} onProjectChange={() => {}} />);
  expect(html).not.toContain('All Projects');
});
