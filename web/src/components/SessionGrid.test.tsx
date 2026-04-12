import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { SessionGrid } from './SessionGrid';
import type { Project, Session } from '../types';
import { SessionStatus } from '../types';

vi.mock('./ProjectGroup', () => ({
  ProjectGroup: ({ project, sessions }: { project: Project; sessions: Session[] }) => (
    <div data-testid={`project-${project.id}`}>
      <span>{project.name}</span>
      <span data-testid={`session-count-${project.id}`}>{sessions.length}</span>
    </div>
  ),
}));

function makeProject(id: string, name: string): Project {
  return { id, name, path: `/workspace/${name}`, runtime: 'claude-code', createdAt: undefined } as any;
}

function makeSession(id: string, projectId: string, score: number): Session {
  return {
    id,
    projectId,
    runtime: 'claude-code',
    status: SessionStatus.RUNNING,
    profile: 'default',
    attentionScore: score,
    startedAt: { seconds: BigInt(1000), nanos: 0 } as any,
    endedAt: undefined,
    lastEventAt: { seconds: BigInt(1001), nanos: 0 } as any,
    pid: BigInt(1),
    tmuxSession: 'gru-test',
    tmuxWindow: 'feat-dev·a1b2c3d4',
    pgid: BigInt(1),
  };
}

describe('SessionGrid', () => {
  it('shows loading state when loading is true', () => {
    render(
      <SessionGrid
        projects={[]}
        sessions={new Map()}
        events={new Map()}
        sessionsSortedByAttention={() => []}
        loading={true}
        connected={true}
      />
    );
    expect(screen.getByText(/loading projects/i)).toBeInTheDocument();
  });

  it('shows empty state when no projects', () => {
    render(
      <SessionGrid
        projects={[]}
        sessions={new Map()}
        events={new Map()}
        sessionsSortedByAttention={() => []}
        loading={false}
        connected={true}
      />
    );
    expect(screen.getByText(/no projects found/i)).toBeInTheDocument();
  });

  it('shows reconnecting banner when not connected', () => {
    const project = makeProject('p1', 'Alpha');
    const session = makeSession('s1', 'p1', 0.5);
    const sessions = new Map([['s1', session]]);

    render(
      <SessionGrid
        projects={[project]}
        sessions={sessions}
        events={new Map()}
        sessionsSortedByAttention={(pid) => pid === 'p1' ? [session] : []}
        loading={false}
        connected={false}
      />
    );
    expect(screen.getByRole('alert')).toHaveTextContent(/reconnecting/i);
  });

  it('renders ProjectGroup for each project that has sessions', () => {
    const p1 = makeProject('p1', 'Alpha');
    const p2 = makeProject('p2', 'Beta');
    const p3 = makeProject('p3', 'Empty');
    const s1 = makeSession('s1', 'p1', 0.9);
    const s2 = makeSession('s2', 'p1', 0.3);
    const s3 = makeSession('s3', 'p2', 0.6);
    const sessions = new Map([['s1', s1], ['s2', s2], ['s3', s3]]);

    const sortFn = (pid: string) => {
      if (pid === 'p1') return [s1, s2];
      if (pid === 'p2') return [s3];
      return [];
    };

    render(
      <SessionGrid
        projects={[p1, p2, p3]}
        sessions={sessions}
        events={new Map()}
        sessionsSortedByAttention={sortFn}
        loading={false}
        connected={true}
      />
    );

    expect(screen.getByTestId('project-p1')).toBeInTheDocument();
    expect(screen.getByTestId('project-p2')).toBeInTheDocument();
    expect(screen.queryByTestId('project-p3')).not.toBeInTheDocument();
  });

  it('shows no-sessions empty state when connected but 0 sessions', () => {
    const project = makeProject('p1', 'Alpha');

    render(
      <SessionGrid
        projects={[project]}
        sessions={new Map()}
        events={new Map()}
        sessionsSortedByAttention={() => []}
        loading={false}
        connected={true}
      />
    );
    expect(screen.getByText(/no active sessions/i)).toBeInTheDocument();
  });
});
