import { renderToStaticMarkup } from 'react-dom/server';
import { expect, it } from 'vitest';
import { ConversationMarkdown } from './conversation-markdown';

// 래퍼 div 의 Tailwind 클래스에 `.katex-display` 문자열이 들어 있어 class 속성 단위로 비교한다
const render = (text: string): string => renderToStaticMarkup(<ConversationMarkdown text={text} />);

it('renders inline math through KaTeX', () => {
  const html = render('면적은 $x^2 + y^2$ 이다');
  expect(html).toContain('class="katex"');
  expect(html).not.toContain('class="katex-display"');
  expect(html).not.toContain('class="katex-error"');
});

it('renders block math as a display equation', () => {
  const html = render('$$\n\\frac{a}{b}\n$$');
  expect(html).toContain('class="katex-display"');
  expect(html).not.toContain('class="katex-error"');
});

it('shows a formula that fails to parse as marked text instead of throwing', () => {
  expect(render('$\\frac{1$')).toMatch(/class="katex-error"[^>]*>\\frac\{1</);
});
