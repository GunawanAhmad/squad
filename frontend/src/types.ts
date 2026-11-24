// Shapes of the Go API (handlers.go).
export type PortraitState = 'idle' | 'thinking' | 'talking';
export interface Character {
  id: string; name: string; model: string; models: string[] | null;
  ui: {
    tagline: string; description: string; accentColor: string; selectLine: string;
    portrait: Record<PortraitState, string>;
    stats: { label: string; value: number }[]; // value out of 10
    voice: { pitch: number; rate: number; blipFrequency: number };
  };
}
export interface ToolCall { name: string; args: string }
export interface Message { role: 'user' | 'assistant'; content: string; tools?: ToolCall[] }

export interface Conversation { id: string; characterId: string; createdAt: string; updatedAt: string; day?: string }

// One row from GET /api/conversations/{id}/messages.
export interface StoredMessage {
  id: number;
  role: 'user' | 'assistant' | 'tool' | 'trigger';
  content: string;
  createdAt: string;
  toolCalls?: { id: string; name: string; arguments: string }[];
  toolCallId?: string;
  runId?: number;
}

// The pre-server chat saved in localStorage, imported once.
export interface LegacyChat { sessionId: string; messages: Message[]; model?: string }

export type StreamEvent =
  | { type: 'text'; delta: string }
  | { type: 'tool'; name: string; args: string }
  | { type: 'error'; message: string }
  | { type: 'done'; runId: number; userMessageId: number; assistantMessageId: number };
