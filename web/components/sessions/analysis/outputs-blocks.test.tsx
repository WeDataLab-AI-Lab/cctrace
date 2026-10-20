import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { TooltipProvider } from '@/components/ui/tooltip';
import type { EditedFiles } from '@/lib/session-analysis-types';
import { EditedFilesBlock } from './outputs-blocks';

const files = (over: Partial<EditedFiles> = {}): EditedFiles => ({
  state: 'observed',
  active_total: 0,
  abandoned_total: 0,
  truncated: false,
  items: [],
  ...over,
});

const renderFiles = (value: EditedFiles): string => renderToStaticMarkup(createElement(
  TooltipProvider,
  null,
  createElement(EditedFilesBlock, { files: value, agent: 'claude' }),
)).replace(/<[^>]+>/g, '');

describe('편집한 파일 요약(D11)', () => {
  it('같은 경로를 두 번 편집하면 파일 한 개와 편집 두 회를 구분한다', () => {
    const html = renderFiles(files({
      active_total: 2,
      items: [{ path: 'fixture.txt', edits: 2, subagent_edits: 0, abandoned_edits: 0 }],
    }));
    expect(html).toContain('파일 1개');
    expect(html).toContain('활성 편집');
    expect(html).toContain('2회');
    expect(html).not.toContain('파일 2개');
  });

  it('서로 다른 관측 경로 수와 편집 횟수를 별도로 센다', () => {
    const html = renderFiles(files({
      active_total: 3,
      items: [
        { path: 'a.txt', edits: 2, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'b.txt', edits: 1, subagent_edits: 0, abandoned_edits: 0 },
      ],
    }));
    expect(html).toContain('파일 2개');
    expect(html).toContain('3회');
  });

  it('잘린 경로 목록은 확인한 파일만 세고 전체 파일 수가 미상임을 알린다', () => {
    const html = renderFiles(files({
      active_total: 5,
      truncated: true,
      items: [{ path: 'fixture.txt', edits: 5, subagent_edits: 0, abandoned_edits: 0 }],
    }));
    expect(html).toContain('확인한 파일 1개');
    expect(html).toContain('전체 파일 수를 알 수 없습니다');
    expect(html).toContain('활성 편집 5회');
  });

  it('잘린 경로와 모르는 편집 합계는 둘 다 미상으로 알린다', () => {
    const html = renderFiles(files({
      active_total: null,
      truncated: true,
      items: [{ path: 'fixture.txt', edits: 1, subagent_edits: 0, abandoned_edits: 0 }],
    }));
    expect(html).toContain('전체 파일 수를 알 수 없습니다');
    expect(html).toContain('활성 편집 횟수 합계도 알 수 없습니다');
    expect(html).not.toContain('활성 편집 횟수는 전체 기준');
  });

  it('경로가 가려져 있고 호출 수도 모르면 파일 수를 만들지 않는다', () => {
    const html = renderFiles(files({ state: 'redacted', active_total: null, abandoned_total: null, truncated: true }));
    expect(html).toContain('경로도 편집 호출 수도 알 수 없습니다');
    expect(html).not.toContain('파일 0개');
  });
});
