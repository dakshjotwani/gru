import { useEffect, useState } from 'react';
import { gruClient } from '../client';
import type { Project } from '../types';

export interface UseProjectsResult {
  projects: Project[];
  loading: boolean;
  error: string | null;
  refetch: () => void;
}

export function useProjects(): UseProjectsResult {
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [fetchCount, setFetchCount] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);

    gruClient
      .listProjects({})
      .then((res) => {
        if (!cancelled) {
          setProjects(res.projects);
          setLoading(false);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err));
          setLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [fetchCount]);

  return {
    projects,
    loading,
    error,
    refetch: () => setFetchCount((c) => c + 1),
  };
}
