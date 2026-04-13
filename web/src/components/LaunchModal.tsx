import { useState, useEffect, useRef, useCallback } from 'react';
import { gruClient } from '../client';
import type { AgentProfile, Project } from '../types';
import styles from './LaunchModal.module.css';

interface LaunchModalProps {
  projects: Project[];
  onClose: () => void;
  onLaunched: () => void;
}

export function LaunchModal({ projects, onClose, onLaunched }: LaunchModalProps) {
  const [projectDir, setProjectDir] = useState('');
  const [useCustomDir, setUseCustomDir] = useState(false);
  const [name, setName] = useState('');
  const [nameManuallyEdited, setNameManuallyEdited] = useState(false);
  const [suggestedName, setSuggestedName] = useState('');
  const [suggestedDesc, setSuggestedDesc] = useState('');
  const [prompt, setPrompt] = useState('');
  const [description, setDescription] = useState('');
  const [profile, setProfile] = useState('');
  const [profiles, setProfiles] = useState<AgentProfile[]>([]);
  const [launching, setLaunching] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const nameRef = useRef<HTMLInputElement>(null);
  const customDirRef = useRef<HTMLInputElement>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Fetch AI-powered name suggestion (debounced 300ms).
  const fetchSuggestion = useCallback(async (p: string, dir: string) => {
    if (!p.trim()) {
      setSuggestedName('');
      setSuggestedDesc('');
      return;
    }
    try {
      const resp = await gruClient.suggestSessionName({ prompt: p, projectDir: dir });
      if (resp.name) {
        setSuggestedName(resp.name);
        setSuggestedDesc(resp.description);
        if (!nameManuallyEdited) {
          setName(resp.name);
        }
      } else {
        setSuggestedName('');
        setSuggestedDesc('');
      }
    } catch {
      // Suggestion is optional — ignore errors silently.
    }
  }, [nameManuallyEdited]);

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      fetchSuggestion(prompt, projectDir);
    }, 300);
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [prompt, projectDir, fetchSuggestion]);

  function handleAcceptSuggestion() {
    setName(suggestedName);
    if (suggestedDesc && !description) {
      setDescription(suggestedDesc);
    }
    setNameManuallyEdited(false);
  }

  // Fetch agent profiles when a project dir is selected.
  useEffect(() => {
    if (!projectDir) {
      setProfiles([]);
      setProfile('');
      return;
    }
    gruClient
      .listProfiles({ projectDir })
      .then((res) => {
        setProfiles(res.profiles);
        setProfile('');
      })
      .catch(() => {
        setProfiles([]);
        setProfile('');
      });
  }, [projectDir]);

  // Focus the first input on open.
  useEffect(() => {
    if (projects.length === 0 || useCustomDir) {
      customDirRef.current?.focus();
    } else {
      nameRef.current?.focus();
    }
  }, [projects.length, useCustomDir]);

  // Close on Escape.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!projectDir.trim()) {
      setError('Project directory is required');
      return;
    }
    if (!name.trim()) {
      setError('Session name is required');
      return;
    }
    if (!prompt.trim()) {
      setError('Prompt is required');
      return;
    }
    setLaunching(true);
    setError(null);
    try {
      await gruClient.launchSession({
        projectDir: projectDir.trim(),
        name: name.trim(),
        prompt: prompt.trim(),
        description: description.trim(),
        profile: profile,
      });
      onLaunched();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setLaunching(false);
    }
  }

  const selectedProject = !useCustomDir ? projects.find((p) => p.path === projectDir) : null;
  const selectedProfileInfo = profiles.find((p) => p.name === profile);
  const showSuggestionHint = suggestedName && suggestedName !== name && nameManuallyEdited;

  return (
    <div className={styles.backdrop} onClick={onClose}>
      <div className={styles.modal} onClick={(e) => e.stopPropagation()} role="dialog" aria-modal="true" aria-label="Launch session">
        <div className={styles.header}>
          <h2 className={styles.title}>Launch session</h2>
          <button className={styles.closeBtn} onClick={onClose} aria-label="Close">×</button>
        </div>

        <form onSubmit={handleSubmit} className={styles.form}>
          {/* Project */}
          <div className={styles.field}>
            <label className={styles.label}>Project</label>
            {!useCustomDir && projects.length > 0 ? (
              <div className={styles.projectSelect}>
                <select
                  className={styles.select}
                  value={projectDir}
                  onChange={(e) => setProjectDir(e.target.value)}
                  disabled={launching}
                >
                  <option value="">— select a project —</option>
                  {projects.map((p) => (
                    <option key={p.id} value={p.path}>
                      {p.name}
                    </option>
                  ))}
                </select>
                <button
                  type="button"
                  className={styles.customLink}
                  onClick={() => { setUseCustomDir(true); setProjectDir(''); }}
                >
                  Custom path
                </button>
              </div>
            ) : (
              <div className={styles.projectSelect}>
                <input
                  ref={customDirRef}
                  className={styles.input}
                  type="text"
                  placeholder="/path/to/project"
                  value={projectDir}
                  onChange={(e) => setProjectDir(e.target.value)}
                  disabled={launching}
                  spellCheck={false}
                />
                {projects.length > 0 && (
                  <button
                    type="button"
                    className={styles.customLink}
                    onClick={() => { setUseCustomDir(false); setProjectDir(''); }}
                  >
                    Known projects
                  </button>
                )}
              </div>
            )}
            {selectedProject && (
              <span className={styles.hint}>{selectedProject.path}</span>
            )}
          </div>

          {/* Agent profile — only shown when the project has profiles configured */}
          {profiles.length > 0 && (
            <div className={styles.field}>
              <label className={styles.label}>Profile <span className={styles.optional}>(optional)</span></label>
              <select
                className={styles.select}
                value={profile}
                onChange={(e) => setProfile(e.target.value)}
                disabled={launching}
              >
                <option value="">— no profile —</option>
                {profiles.map((p) => (
                  <option key={p.name} value={p.name}>
                    {p.name}{p.model ? ` (${p.model})` : ''}
                  </option>
                ))}
              </select>
              {selectedProfileInfo?.description && (
                <span className={styles.hint}>{selectedProfileInfo.description}</span>
              )}
            </div>
          )}

          {/* Prompt */}
          <div className={styles.field}>
            <label className={styles.label}>Prompt <span className={styles.required}>*</span></label>
            <textarea
              className={styles.textarea}
              placeholder="What should the agent do?"
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              disabled={launching}
              rows={4}
            />
          </div>

          {/* Session name */}
          <div className={styles.field}>
            <label className={styles.label}>Session name <span className={styles.required}>*</span></label>
            <input
              ref={nameRef}
              className={styles.input}
              type="text"
              placeholder="e.g. auth-bugfix"
              value={name}
              onChange={(e) => { setName(e.target.value); setNameManuallyEdited(true); }}
              onBlur={() => { if (!name.trim()) setNameManuallyEdited(false); }}
              disabled={launching}
              spellCheck={false}
            />
            {showSuggestionHint && (
              <span className={styles.suggestion}>
                Suggested: <em>{suggestedName}</em>{' '}
                <button
                  type="button"
                  className={styles.acceptBtn}
                  onClick={handleAcceptSuggestion}
                >
                  Accept
                </button>
              </span>
            )}
          </div>

          {/* Description (optional) */}
          <div className={styles.field}>
            <label className={styles.label}>Description <span className={styles.optional}>(optional)</span></label>
            <input
              className={styles.input}
              type="text"
              placeholder="Brief context about the problem"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              disabled={launching}
            />
          </div>

          {error && <p className={styles.error}>{error}</p>}

          <div className={styles.actions}>
            <button type="button" className={styles.cancelBtn} onClick={onClose} disabled={launching}>
              Cancel
            </button>
            <button type="submit" className={styles.launchBtn} disabled={launching}>
              {launching ? 'Launching...' : 'Launch'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
