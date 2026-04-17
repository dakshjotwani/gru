import { useState, useEffect, useRef } from 'react';
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
  const [addDirs, setAddDirs] = useState<string[]>([]);
  const [saveWorkdirs, setSaveWorkdirs] = useState(true);
  const [launching, setLaunching] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const nameRef = useRef<HTMLInputElement>(null);
  const customDirRef = useRef<HTMLInputElement>(null);

  // Fetch AI-powered name suggestion (debounced 300ms, with cancellation).
  useEffect(() => {
    if (!prompt.trim()) {
      setSuggestedName('');
      setSuggestedDesc('');
      return;
    }
    let cancelled = false;
    const timer = setTimeout(async () => {
      try {
        const resp = await gruClient.suggestSessionName({ prompt, projectDir });
        if (cancelled) return;
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
    }, 300);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [prompt, projectDir, nameManuallyEdited]);

  function handleAcceptSuggestion() {
    setName(suggestedName);
    if (suggestedDesc && !description) {
      setDescription(suggestedDesc);
    }
    setNameManuallyEdited(false);
  }

  // Fetch agent profiles when a project dir is selected (debounced + cancelled).
  useEffect(() => {
    if (!projectDir) {
      setProfiles([]);
      setProfile('');
      return;
    }
    let cancelled = false;
    const timer = setTimeout(() => {
      gruClient
        .listProfiles({ projectDir })
        .then((res) => {
          if (!cancelled) {
            setProfiles(res.profiles);
            setProfile('');
          }
        })
        .catch(() => {
          if (!cancelled) {
            setProfiles([]);
            setProfile('');
          }
        });
    }, 300);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [projectDir]);

  // Focus the first input on open.
  useEffect(() => {
    if (projects.length === 0 || useCustomDir) {
      customDirRef.current?.focus();
    } else {
      nameRef.current?.focus();
    }
  }, [projects.length, useCustomDir]);

  // Prefill additional workdirs from the selected project's saved defaults.
  // Custom-path launches start empty — there's no project row yet, so we have
  // nothing to pull from. Resets whenever the selected project changes.
  useEffect(() => {
    if (useCustomDir) {
      setAddDirs([]);
      return;
    }
    const proj = projects.find((p) => p.path === projectDir);
    setAddDirs(proj?.additionalWorkdirs ?? []);
  }, [projectDir, projects, useCustomDir]);

  // Close on Escape.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  // Clear error when the user modifies any field.
  function clearError() {
    if (error) setError(null);
  }

  // Strip connect-rpc noise from error messages for cleaner display.
  function formatError(err: unknown): string {
    const raw = err instanceof Error ? err.message : String(err);
    // connect-rpc errors look like "[invalid_argument] message" — strip the code prefix.
    const cleaned = raw.replace(/^\[[\w_]+\]\s*/, '');
    return cleaned.charAt(0).toUpperCase() + cleaned.slice(1);
  }

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
      const cleanedDirs = addDirs.map((d) => d.trim()).filter((d) => d && d !== projectDir.trim());

      // Persist the workdir list back to the project before launching so
      // future sessions in this project inherit it. Skip when:
      //  - user opted out via the checkbox
      //  - project row doesn't exist yet (custom-path launch)
      //  - the list matches what the project already has (no-op)
      if (saveWorkdirs && !useCustomDir) {
        const selectedProject = projects.find((p) => p.path === projectDir);
        if (selectedProject) {
          const existing = selectedProject.additionalWorkdirs ?? [];
          const same =
            existing.length === cleanedDirs.length &&
            existing.every((v, i) => v === cleanedDirs[i]);
          if (!same) {
            try {
              await gruClient.updateProject({
                id: selectedProject.id,
                additionalWorkdirs: cleanedDirs,
              });
            } catch (err) {
              setError(formatError(err));
              setLaunching(false);
              return;
            }
          }
        }
      }

      await gruClient.launchSession({
        projectDir: projectDir.trim(),
        name: name.trim(),
        prompt: prompt.trim(),
        description: description.trim(),
        profile: profile,
        addDirs: cleanedDirs,
      });
      onLaunched();
      onClose();
    } catch (err) {
      setError(formatError(err));
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
                  onChange={(e) => { setProjectDir(e.target.value); clearError(); }}
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
                  onChange={(e) => { setProjectDir(e.target.value); clearError(); }}
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
                onChange={(e) => { setProfile(e.target.value); clearError(); }}
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

          {/* Additional workdirs — passed to Claude Code as --add-dir <path>.
               Defaults pre-fill from the selected project; edits here are
               persisted back to the project unless "save as default" is off. */}
          <div className={styles.field}>
            <label className={styles.label}>
              Additional workdirs <span className={styles.optional}>(optional)</span>
            </label>
            <div className={styles.workdirList}>
              {addDirs.map((dir, i) => (
                <div key={i} className={styles.workdirRow}>
                  <input
                    className={styles.input}
                    type="text"
                    placeholder="/path/to/secondary-repo"
                    value={dir}
                    onChange={(e) => {
                      const next = [...addDirs];
                      next[i] = e.target.value;
                      setAddDirs(next);
                      clearError();
                    }}
                    disabled={launching}
                    spellCheck={false}
                  />
                  <button
                    type="button"
                    className={styles.workdirRemoveBtn}
                    onClick={() => setAddDirs(addDirs.filter((_, idx) => idx !== i))}
                    disabled={launching}
                    aria-label={`Remove workdir ${dir}`}
                    title="Remove"
                  >
                    ×
                  </button>
                </div>
              ))}
              <button
                type="button"
                className={styles.workdirAddBtn}
                onClick={() => setAddDirs([...addDirs, ''])}
                disabled={launching}
              >
                + add workdir
              </button>
            </div>
            {!useCustomDir && projectDir && (
              <label className={styles.toggleInline}>
                <input
                  type="checkbox"
                  checked={saveWorkdirs}
                  onChange={() => setSaveWorkdirs((v) => !v)}
                  disabled={launching}
                />
                <span className={styles.toggleInlineLabel}>
                  Save this list as the project's default
                </span>
              </label>
            )}
          </div>

          {/* Prompt */}
          <div className={styles.field}>
            <label className={styles.label}>Prompt <span className={styles.required}>*</span></label>
            <textarea
              className={styles.textarea}
              placeholder="What should the agent do?"
              value={prompt}
              onChange={(e) => { setPrompt(e.target.value); clearError(); }}
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
              onChange={(e) => { setName(e.target.value); setNameManuallyEdited(true); clearError(); }}
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
              onChange={(e) => { setDescription(e.target.value); clearError(); }}
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
