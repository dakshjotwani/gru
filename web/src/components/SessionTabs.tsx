import { useEffect, useMemo, useState } from 'react';
import type { Session, Artifact, SessionLink } from '../types';
import { TerminalPanel } from './TerminalPanel';
import { ArtifactView } from './ArtifactView';
import { useSessionArtifacts } from '../hooks/useSessionArtifacts';
import { resolveServerUrl } from '../utils/serverUrl';
import styles from './SessionTabs.module.css';

interface SessionTabsProps {
  session: Session;
  /** Parent passes its terminal-focus ref through. */
  focusRef?: React.RefObject<(() => void) | null>;
  fullscreen?: boolean;
  onToggleFullscreen?: () => void;
}

// Tab 0 is always Terminal. Subsequent tabs are artifacts sorted by
// created_at ascending so positions stay stable as new ones arrive.
type ActiveTab = { kind: 'terminal' } | { kind: 'artifact'; id: string };

// hostnameIcon maps known origins to a small emoji glyph. We deliberately
// keep this client-side and minimal — adding a new vendor here is a one-
// line change. Unknown hosts fall back to a generic 🔗.
const HOSTNAME_ICONS: Record<string, string> = {
  'github.com': '🐙',
  'gitlab.com': '🦊',
  'slack.com': '💬',
  'figma.com': '🎨',
  'linear.app': '📋',
  'notion.so': '📝',
  'www.notion.so': '📝',
};

function iconFor(rawUrl: string): string {
  try {
    const u = new URL(rawUrl);
    const host = u.hostname.toLowerCase();
    // Try exact match, then strip leading "www." for tolerance.
    if (HOSTNAME_ICONS[host]) return HOSTNAME_ICONS[host];
    const stripped = host.startsWith('www.') ? host.slice(4) : host;
    if (HOSTNAME_ICONS[stripped]) return HOSTNAME_ICONS[stripped];
    // Sub-domain matches: "foo.notion.so" → notion.
    for (const known of Object.keys(HOSTNAME_ICONS)) {
      if (host.endsWith('.' + known)) return HOSTNAME_ICONS[known];
    }
  } catch { /* malformed URL — server validates schemes, but be defensive */ }
  return '🔗';
}

// shortMime returns a tiny label like "PDF" / "MD" so the tab label stays
// compact even when the title is long.
function shortMime(mime: string): string {
  if (mime === 'application/pdf') return 'PDF';
  if (mime === 'text/markdown') return 'MD';
  return mime.split('/')[1]?.toUpperCase() ?? mime;
}

export function SessionTabs({ session, focusRef, fullscreen, onToggleFullscreen }: SessionTabsProps) {
  const { artifacts, links, error, removeArtifact } = useSessionArtifacts(session.id);
  const [active, setActive] = useState<ActiveTab>({ kind: 'terminal' });
  const [openMenuId, setOpenMenuId] = useState<string | null>(null);

  // If the active artifact tab is deleted (or the session changes), fall
  // back to Terminal.
  useEffect(() => {
    if (active.kind === 'artifact' && !artifacts.some((a) => a.id === active.id)) {
      setActive({ kind: 'terminal' });
    }
  }, [artifacts, active]);

  // Reset back to Terminal whenever the operator opens a different session.
  // We never auto-switch between tabs within the same session — keystrokes
  // must keep going wherever the operator is looking.
  useEffect(() => {
    setActive({ kind: 'terminal' });
  }, [session.id]);

  // Close any open menu on outside click / Escape.
  useEffect(() => {
    if (!openMenuId) return;
    const onClick = () => setOpenMenuId(null);
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpenMenuId(null); };
    document.addEventListener('click', onClick);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('click', onClick);
      document.removeEventListener('keydown', onKey);
    };
  }, [openMenuId]);

  const sortedArtifacts = useMemo(() => {
    return [...artifacts].sort((a, b) => {
      const at = a.createdAt?.seconds ? Number(a.createdAt.seconds) : 0;
      const bt = b.createdAt?.seconds ? Number(b.createdAt.seconds) : 0;
      return at - bt;
    });
  }, [artifacts]);

  const handleDelete = async (art: Artifact) => {
    if (!window.confirm(`Delete "${art.title}"? This cannot be undone.`)) return;
    await removeArtifact(art.id);
    setOpenMenuId(null);
  };

  return (
    <div className={[styles.panel, fullscreen ? styles.panelFullscreen : ''].filter(Boolean).join(' ')}>
      <div className={styles.tabBar} role="tablist" aria-label="Session tabs">
        <button
          type="button"
          role="tab"
          aria-selected={active.kind === 'terminal'}
          className={[styles.tab, active.kind === 'terminal' ? styles.tabActive : ''].filter(Boolean).join(' ')}
          onClick={() => setActive({ kind: 'terminal' })}
        >
          <span className={styles.tabName}>Terminal</span>
        </button>
        {sortedArtifacts.map((art) => (
          <ArtifactTab
            key={art.id}
            artifact={art}
            active={active.kind === 'artifact' && active.id === art.id}
            onClick={() => setActive({ kind: 'artifact', id: art.id })}
            menuOpen={openMenuId === art.id}
            onToggleMenu={(e: React.MouseEvent) => {
              e.stopPropagation();
              setOpenMenuId(openMenuId === art.id ? null : art.id);
            }}
            onDelete={() => handleDelete(art)}
          />
        ))}
        {onToggleFullscreen && (
          <button
            type="button"
            className={styles.fullscreenBtn}
            onClick={onToggleFullscreen}
            title={fullscreen ? 'Exit fullscreen (Esc)' : 'Fullscreen (Ctrl+Shift+F)'}
            aria-label={fullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
          >
            {fullscreen ? '↙' : '⤢'}
          </button>
        )}
      </div>
      {links.length > 0 && <LinkRow links={links} />}
      {error && <div className={styles.errorBanner}>{error}</div>}
      <div className={styles.tabContent}>
        {active.kind === 'terminal' ? (
          <TerminalPanelInner
            session={session}
            focusRef={focusRef}
          />
        ) : (
          <ArtifactView artifact={artifacts.find((a) => a.id === active.id)!} />
        )}
      </div>
    </div>
  );
}

// TerminalPanelInner is the existing TerminalPanel rendered without its
// own title bar — the parent SessionTabs draws the tab bar instead.
function TerminalPanelInner({ session, focusRef }: { session: Session; focusRef?: React.RefObject<(() => void) | null> }) {
  return <TerminalPanel session={session} focusRef={focusRef} hideTitleBar />;
}

interface ArtifactTabProps {
  artifact: Artifact;
  active: boolean;
  onClick: () => void;
  menuOpen: boolean;
  onToggleMenu: (e: React.MouseEvent) => void;
  onDelete: () => void;
}

function ArtifactTab({ artifact, active, onClick, menuOpen, onToggleMenu, onDelete }: ArtifactTabProps) {
  const downloadHref = resolveServerUrl().replace(/\/+$/, '') + artifact.url;
  return (
    <div
      role="tab"
      aria-selected={active}
      className={[styles.tab, active ? styles.tabActive : ''].filter(Boolean).join(' ')}
      onClick={onClick}
    >
      <span className={styles.tabName}>{artifact.title}</span>
      <span className={styles.tabMime}>{shortMime(artifact.mimeType)}</span>
      <button
        type="button"
        className={styles.tabMenu}
        onClick={onToggleMenu}
        aria-label={`More actions for ${artifact.title}`}
      >
        …
      </button>
      {menuOpen && (
        <div className={styles.menu} onClick={(e) => e.stopPropagation()}>
          <a href={downloadHref} target="_blank" rel="noopener noreferrer">Open in new tab</a>
          <button type="button" className={styles.menuDanger} onClick={onDelete}>Delete</button>
        </div>
      )}
    </div>
  );
}

function LinkRow({ links }: { links: SessionLink[] }) {
  return (
    <div className={styles.linkRow}>
      {links.map((l) => (
        <a
          key={l.id}
          href={l.url}
          target="_blank"
          rel="noopener noreferrer nofollow"
          className={styles.linkChip}
          title={l.url}
        >
          <span className={styles.linkIcon}>{iconFor(l.url)}</span>
          <span>{l.title}</span>
        </a>
      ))}
    </div>
  );
}
