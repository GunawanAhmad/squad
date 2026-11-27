<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { blip, play } from '../audio';
import ChatMessage from './ChatMessage.vue';
import GameButton from './GameButton.vue';
import type { Character, Message, PortraitState, Room, StoredMessage, StreamEvent } from '../types';

const props = defineProps<{ room: Room; chars: Character[] }>();
const emit = defineEmits<{ back: [] }>();

// A room message is a normal chat message that remembers who said it.
interface RoomMessage extends Message { speaker?: string }

const messages = ref<RoomMessage[]>([]);
const input = ref('');
const busy = ref(false);
const ready = ref(false);
const inputEl = ref<HTMLTextAreaElement | null>(null);
const logEl = ref<HTMLElement | null>(null);
let controller: AbortController | null = null;

const members = computed(() =>
  props.room.members
    .map((id) => props.chars.find((c) => c.id === id))
    .filter((c): c is Character => Boolean(c)),
);
const accent = computed(() => members.value[0]?.ui.accentColor ?? '#8b7bff');
const subtitle = computed(() => members.value.map((c) => c.name).join(' · '));

function charFor(id?: string): Character | undefined {
  return props.chars.find((c) => c.id === id);
}

function stateFor(i: number): PortraitState {
  if (!busy.value || i !== messages.value.length - 1) return 'idle';
  return messages.value[i].content ? 'talking' : 'thinking';
}

watch(messages, () => { if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight; }, { deep: true, flush: 'post' });

async function api<T>(path: string, init?: Parameters<typeof fetch>[1]): Promise<T> {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...init });
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return (await res.json()) as T;
}

async function load() {
  const res = await api<{ messages: StoredMessage[] }>(`/api/rooms/${props.room.id}/messages`);
  messages.value = res.messages
    .filter((m) => m.role === 'user' || m.role === 'assistant')
    .map((m) => ({ role: m.role as 'user' | 'assistant', content: m.content, speaker: m.speaker }));
}

onMounted(async () => {
  window.addEventListener('keydown', onKey);
  try {
    await load();
  } catch (err) {
    messages.value.push({ role: 'assistant', content: '⚠️ ' + (err as Error).message });
  } finally {
    ready.value = true;
    nextTick(() => inputEl.value?.focus());
  }
});
onUnmounted(() => {
  window.removeEventListener('keydown', onKey);
  controller?.abort();
});

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') back();
}

function back() {
  play('back');
  emit('back');
}

function resize() {
  const el = inputEl.value;
  if (!el) return;
  el.style.height = '';
  el.style.height = el.scrollHeight - el.clientHeight + el.offsetHeight + 'px';
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

  // The user may clear or leave mid-stream, so keep this list.
  const list = messages.value;
  list.push({ role: 'user', content: text });
  busy.value = true;
  controller = new AbortController();
  let reply: RoomMessage | null = null;

  try {
    const res = await fetch(`/api/rooms/${props.room.id}/messages`, {
      method: 'POST',
      signal: controller.signal,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: text }),
    });
    if (!res.ok) throw new Error(await res.text());

    const reader = res.body!.pipeThrough(new TextDecoderStream()).getReader();
    let buf = '';
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += value;
      const lines = buf.split('\n');
      buf = lines.pop()!;
      for (const line of lines.filter(Boolean)) {
        const e: StreamEvent = JSON.parse(line);
        if (e.type === 'speaker') {
          reply = { role: 'assistant', content: '', tools: [], speaker: e.name };
          list.push(reply);
        } else if (e.type === 'text') {
          if (!reply) { // a model that never announced itself still gets a bubble
            reply = { role: 'assistant', content: '', tools: [], speaker: props.room.members[0] };
            list.push(reply);
          }
          reply.content += e.delta;
          const c = charFor(reply.speaker);
          if (c) blip(c.ui.voice.blipFrequency);
        } else if (e.type === 'tool' && reply) {
          reply.tools!.push({ name: e.name, args: e.args, id: e.id });
        } else if (e.type === 'error') {
          throw new Error(e.message);
        }
      }
    }
    play('arrive');
  } catch (err) {
    const e = err as Error;
    if (e.name !== 'AbortError') {
      const last = list.at(-1);
      if (last?.role === 'assistant' && last.speaker) {
        last.content += (last.content ? '\n\n' : '') + '⚠️ ' + e.message;
      } else {
        list.push({ role: 'assistant', content: '⚠️ ' + e.message, speaker: props.room.members[0] });
      }
    }
  } finally {
    // A speaker event with no text (a failed turn) leaves an empty bubble.
    messages.value = list.filter((m) => m.role === 'user' || m.content || m.tools?.length);
    busy.value = false;
  }
}
</script>

<template>
  <section
    class="chat-screen room-screen"
    :style="{ '--accent': accent }"
  >
    <main>
      <header>
        <div class="who">
          <h1>{{ room.name }}</h1>
          <p>{{ subtitle }}</p>
        </div>
        <GameButton
          class="icon"
          aria-label="Back"
          title="Back"
          @click="back"
        >
          ◀
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
          {{ ready ? 'Say something, or @mention who should answer.' : 'Loading…' }}
        </p>
        <ChatMessage
          v-for="(m, i) in messages"
          :key="i"
          :message="m"
          :character="charFor(m.speaker) ?? chars[0]!"
          :show-name="m.role === 'assistant' && messages[i - 1]?.speaker !== m.speaker"
          :state="stateFor(i)"
        />
      </div>

      <form @submit.prevent="send">
        <textarea
          ref="inputEl"
          v-model="input"
          rows="1"
          :placeholder="ready ? 'Message the room…' : 'Loading…'"
          aria-label="Message the room"
          :disabled="!ready"
          @input="resize"
          @keydown.enter="onEnter"
        />
        <GameButton
          variant="primary"
          class="icon"
          aria-label="Send"
          title="Send"
          :disabled="busy || !ready"
        >
          <svg
            width="18"
            height="18"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2.4"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          ><path d="M12 19V5M5 12l7-7 7 7" /></svg>
        </GameButton>
      </form>
    </main>
  </section>
</template>
