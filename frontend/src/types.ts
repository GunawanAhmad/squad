// Shapes of the Go API (handlers.go).
export type PortraitState = 'idle' | 'thinking' | 'talking';
// The speaker id for the user in rooms and fact provenance.
export const USER_SPEAKER = 'me';
export interface Character {
  id: string; name: string; model: string; models: string[] | null;
  discretion: number; // 0-10, how much the character shares about you in a room
  ui: {
    tagline: string; description: string; accentColor: string; selectLine: string;
    portrait: Record<PortraitState, string>;
    stats: { label: string; value: number }[]; // value out of 10
    voice: { pitch: number; rate: number; blipFrequency: number };
  };
}
export interface ToolCall { name: string; args: string; id?: number }
export interface Message { role: 'user' | 'assistant'; content: string; tools?: ToolCall[] }

export interface Conversation { id: string; characterId: string; createdAt: string; updatedAt: string; day?: string }

// A group conversation (handlers_rooms.go). Members are character ids; you are
// always in it but never listed.
export interface Room { id: string; name: string; members: string[]; createdAt: string }

// One row from GET /api/conversations/{id}/messages.
export interface StoredMessage {
  id: number;
  role: 'user' | 'assistant' | 'tool' | 'trigger';
  content: string;
  speaker?: string; // who said it in a room: a character id or 'me'
  createdAt: string;
  toolCalls?: { id: string; name: string; arguments: string }[];
  toolCallId?: string;
  runId?: number;
}

// The pre-server chat saved in localStorage, imported once.
export interface LegacyChat { sessionId: string; messages: Message[]; model?: string }

export type StreamEvent =
  | { type: 'text'; delta: string }
  | { type: 'tool'; name: string; args: string; id?: number }
  | { type: 'error'; message: string }
  | { type: 'speaker'; name: string } // room: a new character bubble starts
  | { type: 'done'; runId?: number; userMessageId?: number; assistantMessageId?: number };

// Phase 1: the daily loop (handlers_records.go, handlers_due.go).
export interface Task {
  id: number; title: string; notes: string; status: 'open' | 'done' | 'dropped';
  plannedOn?: string; dueOn?: string; timesMoved: number;
  completedAt?: string; updatedAt: string; toolCallId?: number;
  projectId?: number;
}
export interface Activity {
  id: number; day: string; description: string; minutes?: number;
  createdAt: string; toolCallId?: number; projectId?: number;
}
export interface Reminder {
  id: number; text: string; remindAt: string; localTime: string; status: string;
  repeat?: string; firedAt?: string; toolCallId?: number;
}
export interface AssistantSettings {
  tz: string; dayStartsAt: string; morningAt: string; eveningAt: string; jobsModel: string;
}
export interface DueResult {
  day: string; conversationId: string;
  reminders: { id: number; text: string; at: string; missed: boolean }[];
  checkin: string; busy: boolean;
}

// Memory screen (handlers_memory.go).
export interface Fact {
  id: number; text: string; category: string; core: boolean;
  status: 'active' | 'superseded' | 'forgotten';
  sourceKind: string; validFrom: string; validTo?: string;
  about: string; toldBy?: string; // who it is about (an id or 'me') and who said it
  supersededBy?: number; sourceMessageId?: number; useCount: number; createdAt: string;
}
export interface MemoryHit {
  id: number; conversationId: string; characterId: string; role: string;
  content: string; day: string; createdAt: string;
}
