<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue';
import { blip, play } from '../audio';
import ChatMessage from './ChatMessage.vue';
import CharacterPortrait from './CharacterPortrait.vue';
import GameButton from './GameButton.vue';
import GameSelect from './GameSelect.vue';
import type {
  Activity, AssistantSettings, Character, Conversation, DueResult, LegacyChat, Message,
  PortraitState, Reminder, StoredMessage, StreamEvent, Task,
} from '../types';

const props = defineProps<{ character: Character }>();
const emit = defineEmits<{ back: [] }>();

const c = props.character;
const isAssistant = c.id === 'assistant';
const POLL_MS = 60_000;

const messages = ref<Message[]>([]);
const input = ref('');
const busy = ref(false);
const ready = ref(false);
const conversationId = ref('');
const model = ref(loadModel());
const inputEl = ref<HTMLTextAreaElement | null>(null);
const logEl = ref<HTMLElement | null>(null);
let controller: AbortController | null = null;

// Daily loop state (assistant only). Trigger messages never reach the UI.
const lastId = ref(0);
const day = ref('');
const pastView = ref(false);
const panelOpen = ref(false);
const notice = ref('');
const plans = ref<Task[]>([]);
const earlier = ref<Task[]>([]);
const logged = ref<Activity[]>([]);
const reminders = ref<Reminder[]>([]);
const pastDays = ref<Conversation[]>([]);
const settings = ref<AssistantSettings | null>(null);
const form = reactive({ morningAt: '08:00', eveningAt: '21:00', tz: '' });
const permission = ref<'unsupported' | 'default' | 'granted' | 'denied'>(
  typeof Notification === 'undefined' ? 'unsupported' : Notification.permission,
);
let pollTimer: number | undefined;
let polling = false;

if (!c.models?.includes(model.value!)) model.value = c.model; // config may have changed since saving

const portraitState = computed<PortraitState>(() => {
  if (!busy.value) return 'idle';
  return messages.value.at(-1)?.content ? 'talking' : 'thinking';
});

// Keep the log pinned to the newest message, including while a reply streams in.
watch(messages, () => { if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight; }, { deep: true, flush: 'post' });

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && !(e.target as Element).closest('.hud')) {
    if (panelOpen.value) panelOpen.value = false;
    else back();
  }
}
function onVisible() {
  if (isAssistant && !document.hidden) void poll();
}

onMounted(async () => {
  window.addEventListener('keydown', onKey);
  document.addEventListener('visibilitychange', onVisible);
  try {
    if (isAssistant) {
      const due = await api<DueResult>('/api/assistant/due');
      day.value = due.day;
      await openThread(due.conversationId);
      await refreshPanel();
      handleReminders(due);
      pollTimer = window.setInterval(() => { void poll(); }, POLL_MS);
    } else {
      try {
        await importLegacy();
      } catch (err) {
        // A failed one-time import must not block chatting; retry next visit.
        messages.value.push({ role: 'assistant', content: '⚠️ Could not import the old chat: ' + (err as Error).message });
      }
      await openConversation();
    }
  } catch (err) {
    messages.value.push({ role: 'assistant', content: '⚠️ ' + (err as Error).message });
  } finally {
    ready.value = true;
    nextTick(() => inputEl.value?.focus());
  }
});
onUnmounted(() => {
  window.removeEventListener('keydown', onKey);
  document.removeEventListener('visibilitychange', onVisible);
  if (pollTimer) window.clearInterval(pollTimer);
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
  const list = await api<Conversation[]>(`/api/conversations?characterId=${encodeURIComponent(c.id)}&limit=1`);
  if (list[0] && await loadMessages(list[0].id)) return useConversation(list[0].id);

  const conv = await api<Conversation>('/api/conversations', {
    method: 'POST',
    body: JSON.stringify({ characterId: c.id }),
  });
  messages.value = [];
  lastId.value = 0;
  useConversation(conv.id);
}

function useConversation(id: string) {
  conversationId.value = id;
  try { localStorage.setItem(convKey(), id); } catch {}
}

async function loadMessages(id: string): Promise<boolean> {
  const res = await fetch(`/api/conversations/${id}/messages?limit=1000`);
  if (res.status === 404) {
    messages.value = [];
    lastId.value = 0;
    return false;
  }
  if (!res.ok) throw new Error(await res.text());
  const data = (await res.json()) as { messages: StoredMessage[] };
  messages.value = toUI(data.messages);
  lastId.value = maxId(data.messages);
  return true;
}

function maxId(stored: StoredMessage[]): number {
  return stored.reduce((max, m) => Math.max(max, m.id), 0);
}

// Tool-only assistant rows and tool results are internal; the trigger messages
// the check-in engine stores are instructions, never shown. Fold the tool labels
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
    if (m.content === '' && !msg.tools?.length) continue; // system rows with no text
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
    const conv = await api<Conversation>('/api/conversations', {
      method: 'POST',
      body: JSON.stringify({ characterId: c.id }),
    });
    messages.value = [];
    lastId.value = 0;
    useConversation(conv.id);
  } catch (err) {
    messages.value.push({ role: 'assistant', content: '⚠️ ' + (err as Error).message });
  }
}

async function api<T>(path: string, init?: Parameters<typeof fetch>[1]): Promise<T> {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...init });
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// ---- daily loop -----------------------------------------------------------

async function poll() {
  if (!isAssistant || polling || !ready.value) return;
  polling = true;
  try {
    await pollDue();
    await syncMessages();
  } catch (err) {
    console.warn('due poll failed:', err);
  } finally {
    polling = false;
  }
}

// pollDue asks the server what is due. It never waits on a model: a check-in
// turn runs in the background and its message arrives on a later poll.
async function pollDue() {
  const due = await api<DueResult>('/api/assistant/due');
  day.value = due.day;
  if (!pastView.value && due.conversationId !== conversationId.value) {
    await openThread(due.conversationId); // a new day started at 04:00
    await refreshPanel();
  }
  handleReminders(due);
}

function handleReminders(due: DueResult) {
  if (!due.reminders.length) return;
  play('arrive');
  for (const r of due.reminders) {
    const clock = new Date(r.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    notify('⏰ ' + r.text, r.missed ? `missed at ${clock}` : `at ${clock}`, true);
  }
  void refreshPanel();
}

async function syncMessages() {
  if (!conversationId.value || busy.value) return;
  const res = await api<{ messages: StoredMessage[] }>(
    `/api/conversations/${conversationId.value}/messages?after=${lastId.value}&limit=200`,
  );
  let lastText = '';
  for (const m of res.messages) {
    lastId.value = Math.max(lastId.value, m.id);
    if (m.role === 'tool' || m.role === 'trigger' || !m.content) continue;
    messages.value.push({ role: m.role === 'assistant' ? 'assistant' : 'user', content: m.content });
    if (m.role === 'assistant') lastText = m.content;
  }
  if (lastText) {
    play('arrive');
    notify('Assistant', lastText, false);
    void refreshPanel(); // a check-in may have recorded plans
  }
}

async function openThread(id: string) {
  controller?.abort();
  busy.value = false;
  await openConversationByID(id);
}

async function openConversationByID(id: string) {
  const res = await api<{ messages: StoredMessage[] }>(`/api/conversations/${id}/messages?limit=1000`);
  messages.value = toUI(res.messages);
  lastId.value = maxId(res.messages);
  useConversation(id);
}

async function openDay(conv: Conversation) {
  if (conv.id === conversationId.value) return;
  pastView.value = true;
  await openConversationByID(conv.id);
  await refreshPanel();
}

async function backToToday() {
  const due = await api<DueResult>('/api/assistant/due');
  pastView.value = false;
  day.value = due.day;
  if (due.conversationId !== conversationId.value) await openConversationByID(due.conversationId);
}

// ---- Today panel ----------------------------------------------------------

async function refreshPanel() {
  if (!isAssistant || !day.value) return;
  try {
    const [p, e, l, r, s, days] = await Promise.all([
      api<{ tasks: Task[] }>(`/api/assistant/tasks?plannedOn=${day.value}`),
      api<{ tasks: Task[] }>(`/api/assistant/tasks?status=open&openBefore=${day.value}&order=moved&limit=15`),
      api<{ activities: Activity[] }>(`/api/assistant/activities?from=${day.value}&to=${day.value}`),
      api<{ reminders: Reminder[] }>('/api/assistant/reminders?status=pending&limit=10'),
      api<AssistantSettings>('/api/assistant/settings'),
      api<Conversation[]>('/api/conversations?characterId=assistant&limit=30'),
    ]);
    plans.value = p.tasks;
    earlier.value = e.tasks;
    logged.value = l.activities;
    reminders.value = r.reminders;
    settings.value = s;
    form.morningAt = s.morningAt;
    form.eveningAt = s.eveningAt;
    form.tz = s.tz;
    pastDays.value = days.filter((d) => d.id !== conversationId.value);
  } catch (err) {
    notice.value = (err as Error).message;
  }
}

async function patchPlan(t: Task, body: Record<string, unknown>) {
  try {
    await api(`/api/assistant/tasks/${t.id}`, { method: 'PATCH', body: JSON.stringify(body) });
    play('lock');
  } catch (err) {
    notice.value = (err as Error).message; // e.g. undo refused because it changed since
  }
  await refreshPanel();
}

function togglePlan(t: Task) {
  void patchPlan(t, { status: t.status === 'done' ? 'open' : 'done' });
}

async function renamePlan(t: Task) {
  const title = prompt('Plan', t.title);
  if (title && title !== t.title) await patchPlan(t, { title });
}

const plurals = { task: 'tasks', activity: 'activities', reminder: 'reminders' } as const;

// undoRecord uses the same undo path as the chat when the row knows which write
// created it, and a plain delete for rows created on the panel itself.
async function undoRecord(kind: 'task' | 'activity' | 'reminder', row: { id: number; toolCallId?: number }) {
  try {
    if (row.toolCallId) {
      await api('/api/assistant/undo', {
        method: 'POST',
        body: JSON.stringify({
          toolCallId: row.toolCallId, kind, id: row.id, conversationId: conversationId.value,
        }),
      });
    } else {
      await api(`/api/assistant/${plurals[kind]}/${row.id}`, { method: 'DELETE' });
    }
    play('back');
    await refreshPanel();
    await syncMessages();
  } catch (err) {
    notice.value = (err as Error).message; // "task #5 changed since (it is now done)"
  }
}

async function undoFromChat(toolCallId: number) {
  try {
    const res = await api<{ undone: string }>('/api/assistant/undo', {
      method: 'POST',
      body: JSON.stringify({ toolCallId, conversationId: conversationId.value }),
    });
    notice.value = '↩ ' + res.undone;
    play('back');
    await refreshPanel();
    await syncMessages();
  } catch (err) {
    notice.value = (err as Error).message;
  }
}

async function saveSettings() {
  try {
    const saved = await api<AssistantSettings>('/api/assistant/settings', {
      method: 'PUT',
      body: JSON.stringify({ morningAt: form.morningAt, eveningAt: form.eveningAt, tz: form.tz }),
    });
    settings.value = saved;
    notice.value = `Check-ins at ${saved.morningAt} and ${saved.eveningAt}.`;
  } catch (err) {
    notice.value = (err as Error).message;
  }
}

async function askNotify() {
  if (permission.value === 'unsupported') return;
  permission.value = await Notification.requestPermission();
}

// notify shows an OS notification when the user asked for one. Reminders always
// (force), new chat messages only when the tab is hidden: the tab itself shows
// them otherwise.
function notify(title: string, body: string, force: boolean) {
  if (permission.value !== 'granted') return;
  if (!force && !document.hidden) return;
  try {
    new Notification(title, { body: body.slice(0, 200), tag: 'assistant' });
  } catch {
    // Some browsers only allow notifications from a service worker; the in-tab
    // message and the sound still happen.
  }
}

// ---- composing ------------------------------------------------------------

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
  if (!text || busy.value || !ready.value || !conversationId.value) return;
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
        } else if (e.type === 'tool') reply.tools!.push({ name: e.name, args: e.args, id: e.id });
        else if (e.type === 'done') lastId.value = Math.max(lastId.value, e.assistantMessageId ?? 0);
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
    if (isAssistant) void refreshPanel();
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
        <template v-if="isAssistant">
          <GameButton
            v-if="pastView"
            @click="backToToday"
          >
            Today
          </GameButton>
          <GameButton
            :variant="panelOpen ? 'primary' : undefined"
            @click="panelOpen = !panelOpen; refreshPanel()"
          >
            {{ day || 'Today' }}
          </GameButton>
          <GameButton
            v-if="permission === 'default'"
            title="Enable desktop notifications"
            @click="askNotify"
          >
            🔔
          </GameButton>
        </template>
        <GameSelect
          v-if="c.models && c.models.length > 1"
          v-model="model"
          :options="c.models"
          aria-label="Model"
          @change="saveModel"
        />
        <GameButton
          v-if="!isAssistant"
          @click="clear"
        >
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
          @undo="undoFromChat"
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

    <!-- The Today panel: direct edits go through the same validation the tools use. -->
    <aside
      v-if="isAssistant && panelOpen"
      class="today-panel"
      aria-label="Today"
    >
      <header class="panel-head">
        <strong>Today · {{ day }}</strong>
        <button
          class="row-btn"
          aria-label="Close"
          @click="panelOpen = false"
        >
          ✕
        </button>
      </header>
      <p
        v-if="notice"
        class="notice"
        @click="notice = ''"
      >
        {{ notice }}
      </p>

      <section>
        <h3>Plans</h3>
        <p
          v-if="!plans.length"
          class="muted"
        >
          Nothing planned yet.
        </p>
        <ul>
          <li
            v-for="t in plans"
            :key="t.id"
            :class="{ done: t.status === 'done' }"
          >
            <input
              type="checkbox"
              :checked="t.status === 'done'"
              :aria-label="'Done: ' + t.title"
              @change="togglePlan(t)"
            >
            <span class="title">{{ t.title }}</span>
            <span
              v-if="t.timesMoved"
              class="muted"
              :title="'Moved ' + t.timesMoved + ' times'"
            >×{{ t.timesMoved }}</span>
            <button
              class="row-btn"
              title="Rename"
              @click="renamePlan(t)"
            >
              ✎
            </button>
            <button
              class="row-btn"
              :title="t.toolCallId ? 'Undo this write' : 'Delete'"
              @click="undoRecord('task', t)"
            >
              ↩
            </button>
          </li>
        </ul>
        <details v-if="earlier.length">
          <summary>Still open from earlier ({{ earlier.length }})</summary>
          <ul>
            <li
              v-for="t in earlier"
              :key="t.id"
            >
              <input
                type="checkbox"
                :checked="false"
                :aria-label="'Done: ' + t.title"
                @change="togglePlan(t)"
              >
              <span class="title">{{ t.title }}</span>
              <span class="muted">{{ t.plannedOn }}</span>
              <button
                class="row-btn"
                title="Move to today"
                @click="patchPlan(t, { plannedOn: day })"
              >
                →
              </button>
              <button
                class="row-btn"
                title="Undo this write"
                @click="undoRecord('task', t)"
              >
                ↩
              </button>
            </li>
          </ul>
        </details>
      </section>

      <section>
        <h3>Logged today</h3>
        <p
          v-if="!logged.length"
          class="muted"
        >
          Nothing logged yet.
        </p>
        <ul>
          <li
            v-for="a in logged"
            :key="a.id"
          >
            <span class="title">{{ a.description }}</span>
            <span
              v-if="a.minutes"
              class="muted"
            >{{ a.minutes }} min</span>
            <button
              class="row-btn"
              title="Undo this write"
              @click="undoRecord('activity', a)"
            >
              ↩
            </button>
          </li>
        </ul>
      </section>

      <section>
        <h3>Reminders</h3>
        <p
          v-if="!reminders.length"
          class="muted"
        >
          None pending.
        </p>
        <ul>
          <li
            v-for="r in reminders"
            :key="r.id"
          >
            <span class="muted">{{ r.localTime }}</span>
            <span class="title">{{ r.text }}</span>
            <span
              v-if="r.repeat"
              class="muted"
            >({{ r.repeat }})</span>
            <button
              class="row-btn"
              title="Undo this write"
              @click="undoRecord('reminder', r)"
            >
              ↩
            </button>
          </li>
        </ul>
      </section>

      <section>
        <h3>Check-ins</h3>
        <label>Morning <input
          v-model="form.morningAt"
          type="time"
        ></label>
        <label>Evening <input
          v-model="form.eveningAt"
          type="time"
        ></label>
        <label>Time zone <input
          v-model="form.tz"
          type="text"
        ></label>
        <GameButton @click="saveSettings">
          Save
        </GameButton>
      </section>

      <section v-if="pastDays.length">
        <h3>Past days</h3>
        <div class="day-list">
          <button
            v-for="d in pastDays"
            :key="d.id"
            class="day-btn"
            @click="openDay(d)"
          >
            {{ d.day }}
          </button>
        </div>
      </section>
    </aside>
  </section>
</template>
