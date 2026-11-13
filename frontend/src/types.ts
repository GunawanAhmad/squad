// Shapes of the Go API (main.go, agent.go).
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
export interface Chat { sessionId: string; messages: Message[]; model?: string }
export type StreamEvent =
  | { type: 'text'; delta: string }
  | { type: 'tool'; name: string; args: string }
  | { type: 'error'; message: string };
