import { useState } from 'react';
import { gruClient } from '../client';
import type { Project } from '../types';
import styles from './LaunchModal.module.css';

interface LaunchModalProps {
  projects: Project[];
  onClose: () => void;
  onLaunched: () => void;
}

// NOTE: this component is a stub after the env-centric launch refactor.
// The previous version built a rich "pick a project dir + manage add_dirs"
// workflow; both of those concepts are gone. Launch is now "pick an env
// spec" + prompt/name/description. A proper redesign lives in the follow-up
// tracked in docs/superpowers/specs/2026-04-17-env-centric-launch-design.md.
//
// In the meantime: the modal offers a bare-minimum launch form that works
// for specs already registered with the server. For everything else, use
// the CLI (`gru launch <env> <prompt> --name X`).
export function LaunchModal({ projects, onClose, onLaunched }: LaunchModalProps) {
  const [envSpec, setEnvSpec] = useState(projects[0]?.id ?? '');
  const [prompt, setPrompt] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [launching, setLaunching] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleLaunch() {
    if (!envSpec) {
      setError('pick a project');
      return;
    }
    if (!name.trim()) {
      setError('name is required');
      return;
    }
    setLaunching(true);
    setError(null);
    try {
      await gruClient.launchSession({
        envSpec,
        prompt,
        name: name.trim(),
        description,
      });
      onLaunched();
      onClose();
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
      setLaunching(false);
    }
  }

  return (
    <div className={styles.overlay} onClick={onClose}>
      <div className={styles.modal} onClick={(e) => e.stopPropagation()}>
        <h2>Launch session</h2>
        <p className={styles.note}>
          The launch modal is being redesigned for the env-centric launch API.
          For now you can pick an existing project and kick off a session; for
          ad-hoc or complex launches, use <code>gru launch</code> from the CLI.
        </p>
        <label>
          Project
          <select
            value={envSpec}
            onChange={(e) => setEnvSpec(e.target.value)}
            disabled={projects.length === 0}
          >
            {projects.length === 0 && <option value="">no projects registered</option>}
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name} ({p.adapter || 'unknown'})
              </option>
            ))}
          </select>
        </label>
        <label>
          Name
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label>
          Description
          <input
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </label>
        <label>
          Prompt
          <textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} rows={6} />
        </label>
        {error && <div className={styles.error}>{error}</div>}
        <div className={styles.actions}>
          <button onClick={onClose} disabled={launching}>Cancel</button>
          <button onClick={handleLaunch} disabled={launching || !envSpec}>
            {launching ? 'Launching…' : 'Launch'}
          </button>
        </div>
      </div>
    </div>
  );
}
