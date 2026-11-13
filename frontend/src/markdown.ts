import { marked } from 'marked';
import DOMPurify from 'dompurify';
import hljs from 'highlight.js/lib/common';

marked.use({ breaks: true });

DOMPurify.addHook('afterSanitizeAttributes', (n) => {
  if (n.tagName === 'A') {
    n.setAttribute('target', '_blank');
    n.setAttribute('rel', 'noopener noreferrer');
  }
});

// Sanitized because replies can quote fetched web pages.
export const renderMarkdown = (text: string) => DOMPurify.sanitize(marked.parse(text, { async: false }));

export function highlight(el: HTMLElement | null) {
  el?.querySelectorAll<HTMLElement>('pre code:not([data-highlighted])').forEach((c) => hljs.highlightElement(c));
}
