// Shapes of the Go API (main.go, agent.go).
export interface Character { id: string; name: string; emoji: string; blurb: string }
export interface ToolCall { name: string; args: string }
export interface Message { role: 'user' | 'assistant'; content: string; tools?: ToolCall[] }
export interface Chat { sessionId: string; messages: Message[] }
export type StreamEvent =
  | { type: 'text'; delta: string }
  | { type: 'tool'; name: string; args: string }
  | { type: 'error'; message: string };
