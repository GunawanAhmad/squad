import type { StreamEvent } from './types';

// readNDJSON decodes a streaming NDJSON response (one JSON event per line) into
// events, so the chat and room screens don't each carry their own reader loop.
export async function* readNDJSON(res: Response): AsyncGenerator<StreamEvent> {
  const reader = res.body!.pipeThrough(new TextDecoderStream()).getReader();
  let buf = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += value;
    const lines = buf.split('\n');
    buf = lines.pop()!; // split always returns at least one element
    for (const line of lines.filter(Boolean)) yield JSON.parse(line) as StreamEvent;
  }
}
