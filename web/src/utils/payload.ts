/**
 * Parse event payload from Claude Code hook JSON.
 * The payload is stored as bytes (Uint8Array) in the proto message.
 */
export interface ParsedPayload {
  toolName?: string;
  /** Human-readable one-liner for the tool action, e.g. "Install npm dependencies" or "src/foo.ts" */
  toolSummary?: string;
  /** Raw command/path for the expanded view */
  toolInput?: string;
  message?: string;
  notificationType?: string;
}

export function parseEventPayload(payload: Uint8Array | string): ParsedPayload {
  try {
    const str = typeof payload === 'string'
      ? payload
      : new TextDecoder().decode(payload);
    if (!str) return {};
    const obj = JSON.parse(str) as Record<string, unknown>;
    const result: ParsedPayload = {};

    if (typeof obj.tool_name === 'string') {
      result.toolName = obj.tool_name;
    }

    if (typeof obj.message === 'string') {
      result.message = obj.message;
    }

    if (typeof obj.notification_type === 'string') {
      result.notificationType = obj.notification_type;
    }

    // Build a human-readable summary from tool_input based on tool type.
    if (obj.tool_input != null && typeof obj.tool_input === 'object') {
      const input = obj.tool_input as Record<string, unknown>;
      const name = result.toolName ?? '';

      if (name === 'Bash') {
        // Prefer the description field (e.g. "Install npm dependencies"),
        // fall back to the raw command for the one-liner.
        if (typeof input.description === 'string' && input.description) {
          result.toolSummary = input.description;
        } else if (typeof input.command === 'string') {
          const cmd = input.command;
          result.toolSummary = cmd.length > 80 ? cmd.slice(0, 80) + '…' : cmd;
        }
        // Always show the full command in the expanded view.
        if (typeof input.command === 'string') {
          result.toolInput = input.command.length > 400
            ? input.command.slice(0, 400) + '…'
            : input.command;
        }
      } else if (name === 'Edit' || name === 'Write' || name === 'Read' || name === 'MultiEdit') {
        const filePath = (input.file_path ?? input.path) as string | undefined;
        if (typeof filePath === 'string') {
          // Show last 2 path segments as the summary (e.g. "server/service.go")
          result.toolSummary = filePath.split('/').slice(-2).join('/');
          result.toolInput = filePath;
        }
      } else if (name === 'WebFetch' || name === 'WebSearch') {
        const url = (input.url ?? input.query) as string | undefined;
        if (typeof url === 'string') {
          result.toolSummary = url.length > 80 ? url.slice(0, 80) + '…' : url;
          result.toolInput = url;
        }
      } else {
        // Generic fallback: truncated JSON
        const raw = JSON.stringify(obj.tool_input);
        result.toolInput = raw.length > 200 ? raw.slice(0, 200) + '…' : raw;
      }
    }

    return result;
  } catch {
    return {};
  }
}
