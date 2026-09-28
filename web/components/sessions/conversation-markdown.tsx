'use client';

import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import remarkMath from 'remark-math';
import remarkBreaks from 'remark-breaks';
import rehypeKatex from 'rehype-katex';
import 'katex/dist/katex.min.css';

interface ConversationMarkdownProps {
  text: string;
}

// Terminal-level markdown for conversation bubbles: bold, headings-as-bold (no size
// change), inline/block code, lists, and GFM tables. Font size stays uniform with the
// surrounding bubble text (no explicit sizes → inherits `text-xs`); single newlines are
// preserved (remark-breaks) so the model's line layout survives, matching how a terminal
// shows the same output. react-markdown sanitizes URLs by default.
const ConversationMarkdown = ({ text }: ConversationMarkdownProps) => (
  <div className="min-w-0 break-words leading-5 space-y-4 [&_li]:my-0 [&_li_p]:my-0 [&_.katex-display]:overflow-x-auto [&_.katex-display]:overflow-y-hidden [&_.katex-display]:py-0.5">
    <ReactMarkdown
      remarkPlugins={[remarkGfm, remarkMath, remarkBreaks]}
      rehypePlugins={[[rehypeKatex, { throwOnError: false, strict: false }]]}
      components={{
        h1: ({ children }) => <div className="font-semibold">{children}</div>,
        h2: ({ children }) => <div className="font-semibold">{children}</div>,
        h3: ({ children }) => <div className="font-semibold">{children}</div>,
        h4: ({ children }) => <div className="font-semibold">{children}</div>,
        h5: ({ children }) => <div className="font-semibold">{children}</div>,
        h6: ({ children }) => <div className="font-semibold">{children}</div>,
        strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
        ul: ({ children }) => <ul className="list-disc pl-4 marker:text-ink-3">{children}</ul>,
        ol: ({ children }) => <ol className="list-decimal pl-4 marker:text-ink-3">{children}</ol>,
        li: ({ children }) => <li className="pl-0.5">{children}</li>,
        code: ({ children }) => <code className="font-mono rounded bg-surface-sunk px-1">{children}</code>,
        pre: ({ children }) => (
          <pre className="overflow-x-auto rounded border border-border bg-surface-sunk px-2 py-1.5 font-mono">{children}</pre>
        ),
        blockquote: ({ children }) => <blockquote className="border-l-2 border-border pl-2 text-ink-2">{children}</blockquote>,
        a: ({ children, href }) => (
          <a href={href} target="_blank" rel="noopener noreferrer" className="text-brand underline underline-offset-2">{children}</a>
        ),
        table: ({ children }) => (
          <div className="w-fit max-w-full overflow-x-auto rounded border border-border">
            <table className="border-collapse text-left">{children}</table>
          </div>
        ),
        th: ({ children }) => <th className="border-b border-border px-2 py-1 font-semibold">{children}</th>,
        td: ({ children }) => <td className="border-t border-surface-sunk px-2 py-1 align-top">{children}</td>,
        hr: () => <hr className="border-border" />,
      }}
    >
      {text}
    </ReactMarkdown>
  </div>
);

export { ConversationMarkdown };

// Claude Code's explanatory mode emits an insight box marked by a U+2605 star + "Insight",
// delimited by backtick-wrapped rule lines. Rendered as-is those rules show up as inline
// code (the model wrapped them in backticks). Detect the block and render it dimmed on a
// left rail instead: the rule lines drop out (their job was ASCII decoration for a
// terminal) and the inner points still markdown-render, just quieter.
const INSIGHT_OPEN = new RegExp(String.fromCharCode(0x2605) + '\\s*Insight');
const RULE_LINE = /^\s*`?[─—-]{5,}`?\s*$/;

const ConversationContent = ({ text }: ConversationMarkdownProps) => {
  const lines = text.split('\n');
  const segments: { insight: boolean; body: string }[] = [];
  let i = 0;
  while (i < lines.length) {
    if (INSIGHT_OPEN.test(lines[i])) {
      i++; // drop the insight opener line (marker + rule)
      const body: string[] = [];
      while (i < lines.length && !RULE_LINE.test(lines[i])) { body.push(lines[i]); i++; }
      if (i < lines.length) i++; // drop the closing rule line
      segments.push({ insight: true, body: body.join('\n') });
    } else {
      const normal: string[] = [];
      while (i < lines.length && !INSIGHT_OPEN.test(lines[i])) { normal.push(lines[i]); i++; }
      segments.push({ insight: false, body: normal.join('\n') });
    }
  }
  return (
    <div className="space-y-4">
      {segments.map((seg, k) => {
        if (!seg.body.trim()) return null;
        if (seg.insight) {
          return (
            <div key={k} className="border-l-2 border-[var(--border-strong)] pl-3 text-ink-3">
              <div className="mb-0.5 font-semibold">Insight</div>
              <ConversationMarkdown text={seg.body} />
            </div>
          );
        }
        return <ConversationMarkdown key={k} text={seg.body} />;
      })}
    </div>
  );
};

export { ConversationContent };
