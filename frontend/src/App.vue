<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue';
import ChatMessage from './components/ChatMessage.vue';
import type { Character, Chat, StreamEvent } from './types';

const chars = ref<Character[]>([]);
const current = ref<Character | null>(null);
const chat = ref<Chat | null>(null);
const input = ref('');
const busy = ref(false);
const inputEl = ref<HTMLTextAreaElement | null>(null);
const logEl = ref<HTMLElement | null>(null);
let controller: AbortController | null = null;

onMounted(async () => {
  chars.value = await (await fetch('/api/characters')).json();
});

// Keep the log pinned to the newest message, including while a reply streams in.
watch(chat, () => { if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight; }, { deep: true, flush: 'post' });

const newChat = (): Chat => ({ sessionId: crypto.randomUUID(), messages: [] });

function load(id: string): Chat {
  try {
    const c = JSON.parse(localStorage.getItem('chat:' + id) ?? 'null');
    if (c && c.messages) return c;
  } catch {}
  return newChat();
}

function save(id: string, c: Chat) {
  try { localStorage.setItem('chat:' + id, JSON.stringify(c)); } catch {}
}

function select(c: Character) {
  controller?.abort();
  current.value = c;
  chat.value = load(c.id);
  busy.value = false;
  nextTick(() => inputEl.value?.focus());
}

function clear() {
  if (!current.value || !confirm(`Clear your chat with ${current.value.name}?`)) return;
  controller?.abort();
  chat.value = newChat();
  busy.value = false;
  save(current.value.id, chat.value);
}

function resize() {
  const el = inputEl.value;
  if (!el) return;
  el.style.height = '';
  el.style.height = el.scrollHeight + 'px';
}

function onEnter(e: KeyboardEvent) {
  if (e.shiftKey || e.isComposing) return;
  e.preventDefault();
  send();
}

async function send() {
  const text = input.value.trim();
  if (!text || !current.value || !chat.value || busy.value) return;
  input.value = '';
  if (inputEl.value) inputEl.value.style.height = '';

  // The user may switch character or clear mid-stream, so pin these.
  const forChar = current.value;
  const forChat = chat.value;
  forChat.messages.push(
    { role: 'user', content: text },
    { role: 'assistant', content: '', tools: [] },
  );
  const reply = forChat.messages.at(-1)!; // reactive proxy, so edits re-render
  save(forChar.id, forChat);
  busy.value = true;
  controller = new AbortController();

  try {
    const res = await fetch('/api/chat', {
      method: 'POST',
      signal: controller.signal,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        characterId: forChar.id,
        sessionId: forChat.sessionId,
        messages: forChat.messages.slice(0, -1).map(({ role, content }) => ({ role, content })),
      }),
    });
    if (!res.ok) throw new Error(await res.text());

    const reader = res.body!.pipeThrough(new TextDecoderStream()).getReader();
    let buf = '';
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += value;
      const lines = buf.split('\n');
      buf = lines.pop()!; // split always returns at least one element
      for (const line of lines.filter(Boolean)) {
        const e: StreamEvent = JSON.parse(line);
        if (e.type === 'text') reply.content += e.delta;
        else if (e.type === 'tool') reply.tools!.push({ name: e.name, args: e.args });
        else if (e.type === 'error') throw new Error(e.message);
      }
    }
  } catch (err) {
    const e = err as Error;
    if (e.name !== 'AbortError') {
      reply.content += (reply.content ? '\n\n' : '') + '⚠️ ' + e.message;
    }
  } finally {
    if (!reply.content && !reply.tools?.length) forChat.messages.splice(-2); // drop the failed turn
    // Skip saving if the chat was cleared meanwhile (stored session changed).
    if (load(forChar.id).sessionId === forChat.sessionId) save(forChar.id, forChat);
    if (chat.value === forChat) busy.value = false;
  }
}
</script>

<template>
  <aside>
    <h1>Characters</h1>
    <nav>
      <button
        v-for="c in chars"
        :key="c.id"
        class="char"
        :aria-current="current?.id === c.id"
        @click="select(c)"
      >
        <span class="emoji">{{ c.emoji }}</span>
        <span>
          <b>{{ c.name }}</b>
          <small>{{ c.blurb }}</small>
        </span>
      </button>
    </nav>
  </aside>

  <main>
    <header>
      <h2>{{ current ? `${current.emoji} ${current.name}` : 'Pick a character' }}</h2>
      <button
        v-if="current"
        @click="clear"
      >
        Clear chat
      </button>
    </header>

    <div
      id="log"
      ref="logEl"
    >
      <p
        v-if="!current"
        class="empty"
      >
        Pick a character to start talking.
      </p>
      <p
        v-else-if="!chat?.messages.length"
        class="empty"
      >
        Say hello to {{ current.name }}.
      </p>
      <ChatMessage
        v-for="(m, i) in chat?.messages ?? []"
        :key="i"
        :message="m"
      />
    </div>

    <form @submit.prevent="send">
      <textarea
        ref="inputEl"
        v-model="input"
        rows="1"
        placeholder="Message…"
        aria-label="Message"
        :disabled="!current"
        @input="resize"
        @keydown.enter="onEnter"
      />
      <button :disabled="!current || busy">
        Send
      </button>
    </form>
  </main>
</template>
