import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import OpenAPIPage from './page';

// An endpoint that takes no parameter says so instead of showing an empty table.
describe('OpenAPIPage', () => {
  it('states that the practices endpoint takes no parameters', () => {
    const markup = renderToStaticMarkup(createElement(OpenAPIPage));
    const start = markup.indexOf('/api/open/v1/practices</code>');
    const card = markup.slice(start, markup.indexOf('</details>', start));

    expect(start).toBeGreaterThan(-1);
    expect(card).toContain('No parameters.');
    expect(card.split('<table').length - 1).toBe(1);
  });
});
