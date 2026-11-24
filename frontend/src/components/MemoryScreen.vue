<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { play } from '../audio';
import GameButton from './GameButton.vue';
import type { Fact, MemoryHit } from '../types';

const emit = defineEmits<{ back: [] }>();

const facts = ref<Fact[]>([]);
const status = ref('active,superseded');
const category = ref('');
const query = ref('');
const profile = ref('');
const notice = ref('');
const history = ref<{ id: number; facts: Fact[] } | null>(null);
const searchQuery = ref('');
const searchHits = ref<MemoryHit[]>([]);
const searchFacts = ref<Fact[]>([]);
const forgetDay = ref('');
const busy = ref(false);

const categories = ['', 'preference', 'person', 'circumstance', 'goal', 'work', 'health', 'general'];

async function api<T>(path: string, init?: Parameters<typeof fetch>[1]): Promise<T> {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...init });
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

async function loadFacts() {
  try {
    const params = new URLSearchParams();
    if (status.value) params.set('status', status.value);
    if (category.value) params.set('category', category.value);
    if (query.value.trim()) params.set('q', query.value.trim());
    facts.value = (await api<{ facts: Fact[] }>(`/api/memory/facts?${params}`)).facts;
  } catch (err) {
    notice.value = (err as Error).message;
  }
}

async function loadProfile() {
  try { profile.value = (await api<{ profile: string }>('/api/memory/profile')).profile; } catch (err) { notice.value = (err as Error).message; }
}

onMounted(async () => {
  await Promise.all([loadFacts(), loadProfile()]);
});

function back() {
  play('back');
  emit('back');
}

async function editFact(f: Fact) {
  const text = prompt('Fact', f.text);
  if (!text || text === f.text) return;
  await patchFact(f, { text });
  await loadFacts();
}

async function patchFact(f: Fact, body: Record<string, unknown>) {
  try {
    await api(`/api/memory/facts/${f.id}`, { method: 'PATCH', body: JSON.stringify(body) });
    play('lock');
  } catch (err) {
    notice.value = (err as Error).message;
  }
}

async function toggleCore(f: Fact) {
  await patchFact(f, { core: !f.core });
  await loadFacts();
}

async function showHistory(f: Fact) {
  try {
    const res = await api<{ history: Fact[] }>(`/api/memory/facts/${f.id}/history`);
    history.value = { id: f.id, facts: res.history };
  } catch (err) {
    notice.value = (err as Error).message;
  }
}

async function forgetFact(f: Fact) {
  if (!confirm(`Forget "${f.text}"? The message it came from is hidden too.`)) return;
  try {
    await api(`/api/memory/facts/${f.id}/forget`, { method: 'POST', body: JSON.stringify({ confirm: true }) });
    if (history.value?.id === f.id) history.value = null;
    await loadFacts();
  } catch (err) {
    notice.value = (err as Error).message;
  }
}

async function saveProfile() {
  try {
    await api('/api/memory/profile', { method: 'PUT', body: JSON.stringify({ profile: profile.value }) });
    notice.value = 'Profile saved.';
  } catch (err) {
    notice.value = (err as Error).message;
  }
}

async function search() {
  const q = searchQuery.value.trim();
  if (!q) return;
  busy.value = true;
  try {
    const res = await api<{ messages: MemoryHit[]; facts: Fact[] }>(
      `/api/memory/search?q=${encodeURIComponent(q)}&limit=20`,
    );
    searchHits.value = res.messages;
    searchFacts.value = res.facts;
  } catch (err) {
    notice.value = (err as Error).message;
  } finally {
    busy.value = false;
  }
}

async function forgetMessage(m: MemoryHit) {
  if (!confirm(`Hide this message from search and context?\n\n${m.content}`)) return;
  try {
    await api(`/api/memory/messages/${m.id}/forget`, { method: 'POST' });
    searchHits.value = searchHits.value.filter((x) => x.id !== m.id);
  } catch (err) {
    notice.value = (err as Error).message;
  }
}

async function forgetWholeDay() {
  if (!forgetDay.value) return;
  if (!confirm(`Forget every message on ${forgetDay.value}?`)) return;
  try {
    const res = await api<{ forgotten: number }>(`/api/memory/days/${forgetDay.value}/forget`, { method: 'POST' });
    notice.value = `${res.forgotten} message${res.forgotten === 1 ? '' : 's'} hidden.`;
    searchHits.value = [];
  } catch (err) {
    notice.value = (err as Error).message;
  }
}
</script>

<template>
  <section
    class="memory-screen"
    style="--accent: #8b7bff"
  >
    <header class="memory-head">
      <h2>MEMORY</h2>
      <GameButton @click="back">
        ◀ Back
      </GameButton>
    </header>

    <p
      v-if="notice"
      class="notice"
      @click="notice = ''"
    >
      {{ notice }}
    </p>

    <div class="memory-grid">
      <section class="memory-panel">
        <h3>Facts</h3>
        <div class="filters">
          <select
            v-model="status"
            aria-label="Status"
            @change="loadFacts"
          >
            <option value="active,superseded">
              current + past
            </option>
            <option value="active">
              current
            </option>
            <option value="superseded">
              superseded
            </option>
            <option value="forgotten">
              forgotten
            </option>
            <option value="all">
              everything
            </option>
          </select>
          <select
            v-model="category"
            aria-label="Category"
            @change="loadFacts"
          >
            <option
              v-for="c in categories"
              :key="c"
              :value="c"
            >
              {{ c || 'all categories' }}
            </option>
          </select>
          <input
            v-model="query"
            type="search"
            placeholder="Search facts…"
            @input="loadFacts"
          >
        </div>

        <ul class="fact-list">
          <li
            v-for="f in facts"
            :key="f.id"
            :class="f.status"
          >
            <div class="fact-text">
              <strong>#{{ f.id }}</strong>
              {{ f.text }}
              <span class="muted">({{ f.category }}, {{ f.validFrom }}<template v-if="f.validTo">–{{ f.validTo }}</template>)</span>
            </div>
            <div class="fact-actions">
              <button
                class="row-btn"
                :title="f.core ? 'Stop keeping it in context' : 'Keep it in context'"
                @click="toggleCore(f)"
              >
                {{ f.core ? '★' : '☆' }}
              </button>
              <button
                class="row-btn"
                title="Edit"
                @click="editFact(f)"
              >
                ✎
              </button>
              <button
                class="row-btn"
                title="History"
                @click="showHistory(f)"
              >
                ⟲
              </button>
              <button
                v-if="f.status !== 'forgotten'"
                class="row-btn"
                title="Forget (also hides its source message)"
                @click="forgetFact(f)"
              >
                🗑
              </button>
            </div>
          </li>
        </ul>
        <p
          v-if="!facts.length"
          class="muted"
        >
          Nothing here.
        </p>

        <div
          v-if="history"
          class="history"
        >
          <h4>History of #{{ history.id }}</h4>
          <ol>
            <li
              v-for="h in history.facts"
              :key="h.id"
            >
              <strong>#{{ h.id }}</strong> {{ h.text }} <span class="muted">({{ h.status }}, {{ h.validFrom }}<template v-if="h.validTo">–{{ h.validTo }}</template>)</span>
            </li>
          </ol>
        </div>
      </section>

      <section class="memory-panel">
        <h3>Search everything</h3>
        <form
          class="filters"
          @submit.prevent="search"
        >
          <input
            v-model="searchQuery"
            type="search"
            placeholder="Words from any conversation…"
          >
          <GameButton
            variant="primary"
            :disabled="busy"
          >
            Search
          </GameButton>
        </form>
        <ul class="fact-list">
          <li
            v-for="m in searchHits"
            :key="'m' + m.id"
          >
            <div class="fact-text">
              <span class="muted">{{ m.day }} · {{ m.characterId }} · {{ m.role }}</span><br>
              {{ m.content }}
            </div>
            <button
              class="row-btn"
              title="Hide this message"
              @click="forgetMessage(m)"
            >
              🗑
            </button>
          </li>
          <li
            v-for="f in searchFacts"
            :key="'f' + f.id"
          >
            <div class="fact-text">
              <span class="muted">fact #{{ f.id }} · {{ f.category }}</span><br>
              {{ f.text }}
            </div>
          </li>
        </ul>
        <div class="filters">
          <input
            v-model="forgetDay"
            type="date"
            aria-label="Day to forget"
          >
          <GameButton @click="forgetWholeDay">
            Forget that day
          </GameButton>
        </div>
      </section>

      <section class="memory-panel wide">
        <h3>Profile (memory/profile.md)</h3>
        <p class="muted">
          Your own notes, always in context. Edit them here or in the file.
        </p>
        <textarea
          v-model="profile"
          rows="12"
          aria-label="Profile"
        />
        <GameButton
          variant="primary"
          @click="saveProfile"
        >
          Save profile
        </GameButton>
      </section>
    </div>
  </section>
</template>
