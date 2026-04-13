/**
 * Parse event payload from Claude Code hook JSON.
 * The payload is stored as bytes (Uint8Array) in the proto message.
 */
export interface ParsedPayload {
  toolName?: string;
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
    const obj = JSON.parse(str);
    const result: ParsedPayload = {};

    if (typeof obj.tool_name === 'string') {
      result.toolName = obj.tool_name;
    }

    if (obj.tool_input != null) {
      const inputStr = typeof obj.tool_input === 'string'
        ? obj.tool_input
        : JSON.stringify(obj.tool_input);
      result.toolInput = inputStr.length > 100 ? inputStr.slice(0, 100) + '...' : inputStr;
    }

    if (typeof obj.message === 'string') {
      result.message = obj.message;
    }

    if (typeof obj.notification_type === 'string') {
      result.notificationType = obj.notification_type;
    }

    return result;
  } catch {
    return {};
  }
}
