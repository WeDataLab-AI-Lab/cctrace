import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { Table, TableBody } from '@/components/ui/table';
import { ListLoadingRows, TableLoadingRows, rowIn } from './loading-rows';

describe('TableLoadingRows', () => {
  it('fills the body with ghost rows across every column, plus the loader row', () => {
    const markup = renderToStaticMarkup(
      <Table><TableBody><TableLoadingRows columns={5} rows={4} /></TableBody></Table>,
    );
    // 4 ghost rows + 1 row holding the bar loader.
    expect(markup.match(/<tr/g)).toHaveLength(5);
    expect(markup.match(/cctrace-shimmer/g)).toHaveLength(20);
    expect(markup).toContain('colSpan="5"');
    expect(markup).toContain('role="status"');
    expect(markup).toContain('Loading');
  });

  // Rows fill in from the top, and the light band crosses them on a slant: every row
  // and cell carries its position, which globals.css turns into the stagger delays.
  it('marks each ghost row and cell with its position for the stagger', () => {
    const markup = renderToStaticMarkup(
      <Table><TableBody><TableLoadingRows columns={2} rows={3} /></TableBody></Table>,
    );
    expect(markup.match(/cctrace-ghost-row/g)).toHaveLength(3);
    for (const row of ['[--i:0]', '[--i:1]', '[--i:2]']) expect(markup).toContain(row);
    expect(markup.match(/\[--j:0\]/g)).toHaveLength(3);
    expect(markup.match(/\[--j:1\]/g)).toHaveLength(3);
    expect(markup).not.toContain('style=');
  });

  // The widths come from a fixed pattern, so the server and client render the same
  // markup and the rows do not jump on hydration.
  it('renders the same markup every time', () => {
    const render = () => renderToStaticMarkup(<Table><TableBody><TableLoadingRows columns={4} /></TableBody></Table>);
    expect(render()).toBe(render());
  });
});

describe('ListLoadingRows', () => {
  it('draws ghost list items with the loader below them', () => {
    const markup = renderToStaticMarkup(<ListLoadingRows rows={3} />);
    expect(markup.match(/cctrace-row-in/g)).toHaveLength(3);
    expect(markup).toContain('cctrace-shimmer');
    expect(markup).toContain('role="status"');
  });
});

describe('rowIn', () => {
  it('gives the first rows the entrance and their position', () => {
    expect(rowIn(0)).toBe('cctrace-row-in [--i:0]');
    expect(rowIn(19)).toBe('cctrace-row-in [--i:19]');
  });

  // Past the first screen, or rows appended by infinite scroll, enter without delay.
  // They keep the class so a re-sort into the top twenty does not restart the entrance.
  it('enters rows past the first twenty without delay', () => {
    expect(rowIn(20)).toBe('cctrace-row-in [--i:0]');
    expect(rowIn(Infinity)).toBe('cctrace-row-in [--i:0]');
  });
});
