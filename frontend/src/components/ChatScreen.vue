<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { blip, play } from '../audio';
import ChatMessage from './ChatMessage.vue';
import CharacterPortrait from './CharacterPortrait.vue';
import GameButton from './GameButton.vue';
import GameSelect from './GameSelect.vue';
import type { Character, Conversation, LegacyChat, Message, PortraitState, StoredMessage, StreamEvent } from '../types';

const props = defineProps<{ character: Character }>();
const emit = defineEmits<{ back: [] }>();

const c = props.character;
const messages = ref<Message[]>([]);
const input = ref('');
const busy = ref(false);
const ready = ref(false);
const conversationId = ref('');
const model = ref(loadModel());
const inputEl = ref<HTMLTextAreaElement | null>(null);
const logEl = ref<HTMLElement | null>(null);
let controller: AbortController | null = null;

if (!c.models?.includes(model.value!)) model.value = c.model; // config may have changed since saving

const portraitState = computed<PortraitState>(() => {
  if (!busy.value) return 'idle';
  return messages.value.at(-1)?.content ? 'talking' : 'thinking';
});

// Keep the log pinned to the newest message, including while a reply streams in.
watch(messages, () => { if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight; }, { deep: true, flush: 'post' });

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && !(e.target as Element).closest('.hud')) back();
}
onMounted(async () => {
  window.addEventListener('keydown', onKey);
  try {
    await importLegacy();
    await openConversation();
  } catch (err) {
    messages.value.push({ role: 'assistant', content: '⚠️ ' + (err as Error).message });
  } finally {
    ready.value = true;
    nextTick(() => inputEl.value?.focus());
  }
});
onUnmounted(() => {
  window.removeEventListener('keydown', onKey);
  controller?.abort(); // leaving mid-reply cancels it, like switching characters used to
});

function back() {
  play('back');
  emit('back');
}

function modelKey() { return 'model:' + c.id; }
function loadModel(): string {
  const saved = localStorage.getItem(modelKey());
  if (saved) return saved;
  try {
    const old = JSON.parse(localStorage.getItem('chat:' + c.id) ?? 'null') as LegacyChat | null;
    if (old?.model && c.models?.includes(old.model)) return old.model; // migrate the old picker value
  } catch {}
  return c.model;
}
function saveModel() {
  try { localStorage.setItem(modelKey(), model.value); } catch {}
}

function convKey() { return 'conv:' + c.id; }
function importedKey() { return 'imported:' + c.id; }

// The pre-server chats lived in localStorage. Send each one to the server once;
// on failure keep it so the next visit retries.
async function importLegacy() {
  if (localStorage.getItem(importedKey())) return;
  let old: LegacyChat | null = null;
  try { old = JSON.parse(localStorage.getItem('chat:' + c.id) ?? 'null'); } catch {}
  if (old?.sessionId && old.messages?.length) {
    const res = await fetch('/api/conversations/import', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        characterId: c.id,
        sessionId: old.sessionId,
        messages: old.messages
          .filter((m) => m.role === 'user' || m.role === 'assistant')
          .map((m) => ({ role: m.role, content: m.content })),
      }),
    });
    if (!res.ok) throw new Error('import: ' + (await res.text()));
  }
  localStorage.setItem(importedKey(), '1');
  localStorage.removeItem('chat:' + c.id);
}

async function openConversation() {
  const saved = localStorage.getItem(convKey());
  if (saved && await loadMessages(saved)) return useConversation(saved);

  // No usable id (first visit, cleared browser storage, old server): show the
  // newest thread, so clearing browser storage doesn't look like losing chat.
  const res = await fetch(`/api/conversations?characterId=${encodeURIComponent(c.id)}&limit=1`);
  if (!res.ok) throw new Error(await res.text());
  const list = (await res.json()) as Conversation[];
  if (list[0] && await loadMessages(list[0].id)) return useConversation(list[0].id);

  const created = await fetch('/api/conversations', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ characterId: c.id }),
  });
  if (!created.ok) throw new Error(await created.text());
  const conv = (await created.json()) as Conversation;
  messages.value = [];
  useConversation(conv.id);
}

function useConversation(id: string) {
  conversationId.value = id;
  try { localStorage.setItem(convKey(), id); } catch {}
}

async function loadMessages(id: string): Promise<boolean> {
  const res = await fetch(`/api/conversations/${id}/messages`);
  if (res.status === 404) {
    messages.value = [];
    return false;
  }
  if (!res.ok) throw new Error(await res.text());
  const data = (await res.json()) as { messages: StoredMessage[] };
  messages.value = toUI(data.messages);
  return true;
}

// Tool-only assistant rows and tool results are internal; fold the tool labels
// into the assistant bubble that follows them.
function toUI(stored: StoredMessage[]): Message[] {
  const out: Message[] = [];
  let pending: { name: string; args: string }[] = [];
  for (const m of stored) {
    if (m.role === 'tool' || m.role === 'trigger') continue;
    const calls = (m.toolCalls ?? []).map((t) => ({ name: t.name, args: t.arguments }));
    if (m.role === 'assistant' && m.content === '' && calls.length) {
      pending.push(...calls);
      continue;
    }
    const msg: Message = { role: m.role, content: m.content };
    if (calls.length) pending.push(...calls);
    if (pending.length) {
      msg.tools = pending;
      pending = [];
    }
    out.push(msg);
  }
  if (pending.length) out.push({ role: 'assistant', content: '', tools: pending });
  return out;
}

async function clear() {
  if (!confirm(`Start a new chat with ${c.name}? Past messages stay stored on the server.`)) return;
  controller?.abort();
  busy.value = false;
  try {
    const res = await fetch('/api/conversations', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ characterId: c.id }),
    });
    if (!res.ok) throw new Error(await res.text());
    const conv = (await res.json()) as Conversation;
    messages.value = [];
    useConversation(conv.id);
  } catch (err) {
    messages.value.push({ role: 'assistant', content: '⚠️ ' + (err as Error).message });
  }
}

function resize() {
  const el = inputEl.value;
  if (!el) return;
  el.style.height = '';
  el.style.height = el.scrollHeight - el.clientHeight + el.offsetHeight + 'px'; // include borders, or it overflows and shows a scrollbar
}

function onEnter(e: KeyboardEvent) {
  if (e.shiftKey || e.isComposing) return;
  e.preventDefault();
  send();
}

async function send() {
  const text = input.value.trim();
  if (!text || busy.value || !ready.value) return;
  input.value = '';
  if (inputEl.value) inputEl.value.style.height = '';

  // The user may clear mid-stream, so pin the list.
  const list = messages.value;
  list.push(
    { role: 'user', content: text },
    { role: 'assistant', content: '', tools: [] },
  );
  const reply = list.at(-1)!; // reactive proxy, so edits re-render
  busy.value = true;
  controller = new AbortController();

  try {
    const res = await fetch(`/api/conversations/${conversationId.value}/messages`, {
      method: 'POST',
      signal: controller.signal,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: text, model: model.value }), // history lives on the server
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
        if (e.type === 'text') {
          reply.content += e.delta;
          blip(c.ui.voice.blipFrequency);
        } else if (e.type === 'tool') reply.tools!.push({ name: e.name, args: e.args });
        else if (e.type === 'error') throw new Error(e.message);
      }
    }
    play('arrive');
  } catch (err) {
    const e = err as Error;
    if (e.name !== 'AbortError') {
      reply.content += (reply.content ? '\n\n' : '') + '⚠️ ' + e.message;
    }
  } finally {
    if (messages.value === list) {
      if (!reply.content && !reply.tools?.length) messages.value.splice(-2); // drop the failed turn
      if (!reply.tools?.length) delete reply.tools;
    }
    if (messages.value === list) busy.value = false;
  }
}
</script>

<template>
  <section
    class="chat-screen"
    :style="{ '--accent': c.ui.accentColor }"
  >
    <div class="bust">
      <CharacterPortrait
        :character="c"
        :state="portraitState"
      />
      <div class="nameplate">
        {{ c.name }}
      </div>
    </div>

    <main>
      <header>
        <GameButton @click="back">
          ◀ Select
        </GameButton>
        <h2>{{ c.name }}</h2>
        <GameSelect
          v-if="c.models && c.models.length > 1"
          v-model="model"
          :options="c.models"
          aria-label="Model"
          @change="saveModel"
        />
        <GameButton @click="clear">
          New chat
        </GameButton>
      </header>

      <div
        id="log"
        ref="logEl"
      >
        <p
          v-if="!messages.length"
          class="empty"
        >
          {{ ready ? `Say hello to ${c.name}.` : 'Loading…' }}
        </p>
        <ChatMessage
          v-for="(m, i) in messages"
          :key="i"
          :message="m"
          :character="c"
        />
      </div>

      <form @submit.prevent="send">
        <textarea
          ref="inputEl"
          v-model="input"
          rows="1"
          :placeholder="ready ? 'Message…' : 'Loading…'"
          aria-label="Message"
          :disabled="!ready"
          @input="resize"
          @keydown.enter="onEnter"
        />
        <GameButton
          variant="primary"
          :disabled="busy || !ready"
        >
          Send
        </GameButton>
      </form>
    </main>
  </section>
</template>
