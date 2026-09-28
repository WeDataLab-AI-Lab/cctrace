import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { safeUrl } from './rule-helpers';

interface MarkdownViewerProps {
  content: string;
}

const MarkdownViewer = ({ content }: MarkdownViewerProps) => {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      urlTransform={safeUrl}
      components={{
        h1: ({ children }) => <h1 className="text-[20px] font-semibold leading-7 text-ink">{children}</h1>,
        h2: ({ children }) => <h2 className="pt-1 text-[15px] font-semibold leading-6 text-ink">{children}</h2>,
        h3: ({ children }) => <h3 className="text-[13px] font-semibold leading-6 text-ink">{children}</h3>,
        p: ({ children }) => <p className="text-[13px] leading-6 text-ink-2">{children}</p>,
        ul: ({ children }) => <ul className="space-y-1 pl-4 text-[13px] leading-6 list-disc marker:text-brand">{children}</ul>,
        ol: ({ children }) => <ol className="space-y-1 pl-4 text-[13px] leading-6 list-decimal marker:text-brand">{children}</ol>,
        li: ({ children }) => <li className="pl-1 text-ink-2">{children}</li>,
        blockquote: ({ children }) => <blockquote className="border-l-2 border-brand pl-3 text-ink-2">{children}</blockquote>,
        pre: ({ children }) => (
          <pre className="overflow-x-auto rounded-md border border-border bg-surface-sunk px-3 py-2 font-mono text-[12px] leading-5 text-ink-2">
            {children}
          </pre>
        ),
        code: ({ children }) => <code className="font-mono text-[12px]">{children}</code>,
        table: ({ children }) => (
          <div className="overflow-x-auto rounded-md border border-border">
            <table className="min-w-full border-collapse text-left text-[12px] leading-5">{children}</table>
          </div>
        ),
        thead: ({ children }) => <thead className="bg-canvas text-ink">{children}</thead>,
        th: ({ children }) => <th className="border-b border-border px-3 py-2 font-semibold">{children}</th>,
        td: ({ children }) => <td className="border-t border-surface-sunk px-3 py-2 align-top text-ink-2">{children}</td>,
        a: ({ children, href }) => {
          const safe = safeUrl(href || '');
          if (!safe) return <span className="text-ink-2">{children}</span>;
          return (
            <a href={safe} target="_blank" rel="noopener noreferrer" className="text-brand underline underline-offset-2">
              {children}
            </a>
          );
        },
        hr: () => <hr className="border-border" />,
      }}
    >
      {content}
    </ReactMarkdown>
  );
};

export { MarkdownViewer };
