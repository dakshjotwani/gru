import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { KillButton } from './KillButton';

vi.mock('../client', () => ({
  gruClient: {
    killSession: vi.fn(),
  },
}));

import { gruClient } from '../client';

describe('KillButton', () => {
  const SESSION_ID = 'abcdef12-1234-5678-abcd-ef1234567890';

  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders a Kill button initially', () => {
    render(<KillButton sessionId={SESSION_ID} />);
    expect(screen.getByRole('button', { name: /kill session abcdef12/i })).toBeInTheDocument();
  });

  it('shows confirmation dialog when clicked', () => {
    render(<KillButton sessionId={SESSION_ID} />);
    fireEvent.click(screen.getByRole('button', { name: /kill session abcdef12/i }));
    expect(screen.getByText(/kill session abcdef12\?/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /confirm kill/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /cancel/i })).toBeInTheDocument();
  });

  it('calls killSession and invokes onKilled on confirm', async () => {
    const onKilled = vi.fn();
    vi.mocked(gruClient.killSession).mockResolvedValue({} as any);

    render(<KillButton sessionId={SESSION_ID} onKilled={onKilled} />);
    fireEvent.click(screen.getByRole('button', { name: /kill session abcdef12/i }));
    fireEvent.click(screen.getByRole('button', { name: /confirm kill/i }));

    await waitFor(() => {
      expect(gruClient.killSession).toHaveBeenCalledWith({ id: SESSION_ID });
      expect(onKilled).toHaveBeenCalledOnce();
    });
  });

  it('does not call killSession when cancel is clicked', () => {
    render(<KillButton sessionId={SESSION_ID} />);
    fireEvent.click(screen.getByRole('button', { name: /kill session abcdef12/i }));
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }));
    expect(gruClient.killSession).not.toHaveBeenCalled();
    expect(screen.queryByText(/kill session abcdef12\?/i)).not.toBeInTheDocument();
  });

  it('shows error message when killSession rejects', async () => {
    vi.mocked(gruClient.killSession).mockRejectedValue(new Error('connection refused'));

    render(<KillButton sessionId={SESSION_ID} />);
    fireEvent.click(screen.getByRole('button', { name: /kill session abcdef12/i }));
    fireEvent.click(screen.getByRole('button', { name: /confirm kill/i }));

    await waitFor(() => {
      expect(screen.getByText('connection refused')).toBeInTheDocument();
    });
  });
});
