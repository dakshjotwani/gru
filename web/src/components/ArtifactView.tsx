import { useEffect, useState } from 'react';
import DOMPurify from 'dompurify';
import MarkdownIt from 'markdown-it';
import type { Artifact } from '../types';
import { resolveServerUrl } from '../utils/serverUrl';
import styles from './SessionTabs.module.css';

// Markdown styles injected into the iframe. Mirrors the Terminal panel's
// palette so artifacts feel like they belong on the same surface.
const MD_STYLE = `
  :root { color-scheme: dark; }
  body {
    margin: 0;
    padding: 24px 32px;
    background: #0d1117;
    color: #c9d1d9;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Helvetica Neue",
                 Arial, "Noto Sans", sans-serif, "Apple Color Emoji", "Segoe UI Emoji";
    font-size: 14px;
    line-height: 1.6;
    max-width: 880px;
  }
  h1, h2, h3, h4, h5, h6 {
    color: #f0f6fc;
    border-bottom: 1px solid #21262d;
    padding-bottom: 4px;
    margin-top: 1.6em;
    margin-bottom: 0.6em;
  }
  h1 { font-size: 1.6em; }
  h2 { font-size: 1.35em; }
  h3 { font-size: 1.15em; border-bottom: none; }
  a { color: #58a6ff; }
  a:hover { text-decoration: underline; }
  p { margin: 0.6em 0; }
  ul, ol { padding-left: 1.6em; }
  li { margin: 0.2em 0; }
  blockquote {
    border-left: 3px solid #30363d;
    padding: 0.2em 1em;
    color: #8b949e;
    margin: 0.6em 0;
  }
  code {
    background: #161b22;
    color: #c9d1d9;
    padding: 0.15em 0.35em;
    border-radius: 4px;
    font-family: "JetBrains Mono", "Fira Code", "SF Mono", monospace;
    font-size: 0.92em;
  }
  pre {
    background: #161b22;
    border: 1px solid #21262d;
    border-radius: 6px;
    padding: 12px 14px;
    overflow-x: auto;
    line-height: 1.45;
  }
  pre code {
    background: transparent;
    padding: 0;
    border-radius: 0;
    font-size: 0.85em;
  }
  table {
    border-collapse: collapse;
    margin: 0.6em 0;
  }
  th, td {
    border: 1px solid #30363d;
    padding: 4px 10px;
  }
  th { background: #161b22; color: #f0f6fc; }
  hr { border: none; border-top: 1px solid #21262d; margin: 1.2em 0; }
  img { max-width: 100%; height: auto; }
`;

// Markdown renderer is configured per the design spec: no raw HTML
// passthrough, hyperlink autolinking on, hard breaks off (so a single
// newline behaves as in standard Markdown — only blank lines split paras).
const md = new MarkdownIt({ html: false, linkify: true, breaks: false });

interface ArtifactViewProps {
  artifact: Artifact;
}

// ArtifactView dispatches per MIME type. PDF embeds directly in a
// sandboxed iframe (browser PDF viewer renders); Markdown is rendered to
// HTML in the parent, sanitized, then injected into a sandboxed iframe
// via srcdoc. Anything else falls back to a download card.
export function ArtifactView({ artifact }: ArtifactViewProps) {
  const downloadHref = resolveServerUrl().replace(/\/+$/, '') + artifact.url;

  if (artifact.mimeType === 'application/pdf') {
    return (
      <iframe
        className={styles.iframe}
        // Empty sandbox = opaque origin: no cookies, no localStorage, no
        // scripts, no top-level navigation, no parent-DOM access. Enough
        // to neutralize any JS embedded in the PDF.
        sandbox=""
        src={downloadHref}
        referrerPolicy="no-referrer"
        title={artifact.title}
      />
    );
  }

  if (artifact.mimeType === 'text/markdown') {
    return <MarkdownView href={downloadHref} title={artifact.title} />;
  }

  // Fallback for MIME types not on the renderer allowlist (the server
  // allowlist gates on this too — no upload should land here today).
  return (
    <div className={styles.fallback}>
      <div>{artifact.title}</div>
      <a href={downloadHref} target="_blank" rel="noopener noreferrer">Download ({artifact.mimeType})</a>
      <div className={styles.fallbackHint}>No inline preview available for this MIME type.</div>
    </div>
  );
}

interface MarkdownViewProps {
  href: string;
  title: string;
}

function MarkdownView({ href, title }: MarkdownViewProps) {
  const [doc, setDoc] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setDoc(null);
    setError(null);
    fetch(href, { credentials: 'omit', mode: 'cors' })
      .then(async (resp) => {
        if (!resp.ok) throw new Error(`fetch failed: ${resp.status}`);
        return resp.text();
      })
      .then((mdText) => {
        if (cancelled) return;
        // Three independent layers in front of the iframe sandbox:
        //   1. markdown-it with html:false escapes raw <script> in the source.
        //   2. DOMPurify strips anything the parser still emitted (on*=, javascript:).
        //   3. The iframe sandbox="" gives the rendered HTML an opaque origin.
        const dirty = md.render(mdText);
        const clean = DOMPurify.sanitize(dirty);
        const html = `<!doctype html><html><head><meta charset="utf-8">` +
                     `<base target="_blank">` +
                     `<style>${MD_STYLE}</style></head>` +
                     `<body>${clean}</body></html>`;
        setDoc(html);
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      });
    return () => { cancelled = true; };
  }, [href]);

  if (error) {
    return (
      <div className={styles.fallback}>
        <div>Couldn’t load {title}</div>
        <div className={styles.fallbackHint}>{error}</div>
        <a href={href} target="_blank" rel="noopener noreferrer">Download</a>
      </div>
    );
  }
  if (doc === null) {
    return <div className={styles.fallback}>Loading…</div>;
  }
  return (
    <iframe
      className={styles.iframe}
      sandbox=""
      srcDoc={doc}
      referrerPolicy="no-referrer"
      title={title}
    />
  );
}
