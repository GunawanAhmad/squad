<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { blip, play } from '../audio';
import ChatMessage from './ChatMessage.vue';
import CharacterPortrait from './CharacterPortrait.vue';
import GameButton from './GameButton.vue';
import GameSelect from './GameSelect.vue';
import type { Character, Chat, PortraitState, StreamEvent } from '../types';

const props = defineProps<{ character: Character }>();
const emit = defineEmits<{ back: [] }>();

const chat = ref<Chat>(load(props.character.id));
const input = ref('');
const busy = ref(false);
const inputEl = ref<HTMLTextAreaElement | null>(null);
const logEl = ref<HTMLElement | null>(null);
let controller: AbortController | null = null;

const c = props.character;
if (!c.models?.includes(chat.value.model!)) chat.value.model = c.model; // config may have changed since saving

const portraitState = computed<PortraitState>(() => {
  if (!busy.value) return 'idle';
  return chat.value.messages.at(-1)?.content ? 'talking' : 'thinking';
});

// Keep the log pinned to the newest message, including while a reply streams in.
watch(chat, () => { if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight; }, { deep: true, flush: 'post' });

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && !(e.target as Element).closest('.hud')) back();
}
onMounted(() => {
  window.addEventListener('keydown', onKey);
  nextTick(() => inputEl.value?.focus());
});
onUnmounted(() => {
  window.removeEventListener('keydown', onKey);
  controller?.abort(); // leaving mid-reply cancels it, like switching characters used to
});

function back() {
  play('back');
  emit('back');
}

function newChat(): Chat { return { sessionId: crypto.randomUUID(), messages: [] }; }

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

function clear() {
  if (!confirm(`Clear your chat with ${c.name}?`)) return;
  controller?.abort();
  chat.value = { ...newChat(), model: chat.value.model };
  busy.value = false;
  save(c.id, chat.value);
}

function resize() {
  const el = inputEl.value;
  if (!el) return;
  el.style.height = '';
  el.style.height = el.scrollHeight + el.offsetHeight - el.clientHeight + 'px'; // include borders, or it overflows and shows a scrollbar
}

function onEnter(e: KeyboardEvent) {
  if (e.shiftKey || e.isComposing) return;
  e.preventDefault();
  send();
}

async function send() {
  const text = input.value.trim();
  if (!text || busy.value) return;
  input.value = '';
  if (inputEl.value) inputEl.value.style.height = '';

  // The user may clear mid-stream, so pin the chat.
  const forChat = chat.value;
  forChat.messages.push(
    { role: 'user', content: text },
    { role: 'assistant', content: '', tools: [] },
  );
  const reply = forChat.messages.at(-1)!; // reactive proxy, so edits re-render
  save(c.id, forChat);
  busy.value = true;
  controller = new AbortController();

  try {
    const res = await fetch('/api/chat', {
      method: 'POST',
      signal: controller.signal,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        characterId: c.id,
        sessionId: forChat.sessionId,
        model: forChat.model,
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
    if (!reply.content && !reply.tools?.length) forChat.messages.splice(-2); // drop the failed turn
    // Skip saving if the chat was cleared meanwhile (stored session changed).
    if (load(c.id).sessionId === forChat.sessionId) save(c.id, forChat);
    if (chat.value === forChat) busy.value = false;
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
          v-model="chat.model"
          :options="c.models"
          aria-label="Model"
          @change="save(c.id, chat)"
        />
        <GameButton @click="clear">
          Clear chat
        </GameButton>
      </header>

      <div
        id="log"
        ref="logEl"
      >
        <p
          v-if="!chat.messages.length"
          class="empty"
        >
          Say hello to {{ c.name }}.
        </p>
        <ChatMessage
          v-for="(m, i) in chat.messages"
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
          placeholder="Message…"
          aria-label="Message"
          @input="resize"
          @keydown.enter="onEnter"
        />
        <GameButton
          variant="primary"
          :disabled="busy"
        >
          Send
        </GameButton>
      </form>
    </main>
  </section>
</template>
