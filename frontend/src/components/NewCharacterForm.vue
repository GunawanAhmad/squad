<script setup lang="ts">
import { computed, ref } from 'vue';
import { api } from '../api';
import { DOLL_OPTIONS, SKIN, dollURL, normalizeDoll, type Doll } from '../paperDoll';
import type { Character, CharacterDraft } from '../types';

const props = defineProps<{ chars: Character[] }>();
const emit = defineEmits<{ created: [Character] }>();

// Only what any non-assistant character may have; the server enforces it too.
const TOOLS = [
  { id: 'web_search', label: 'Web search' },
  { id: 'fetch_url', label: 'Read pages' },
  { id: 'remember', label: 'Remember' },
  { id: 'recall', label: 'Recall' },
  { id: 'forget', label: 'Forget' },
];

// The portrait's traits, editable after the model picks them.
const LOOK = Object.entries(DOLL_OPTIONS) as [keyof typeof DOLL_OPTIONS, readonly string[]][];
const LOOK_LABELS: Record<keyof typeof DOLL_OPTIONS, string> = {
  head: 'Head', build: 'Build', hair: 'Hair style', eyes: 'Eyes', facialHair: 'Facial hair', outfit: 'Outfit', accessory: 'Accessory',
};
const SKIN_LABELS = ['Palest', 'Light', 'Tan', 'Brown', 'Dark', 'Darkest'];
const HAIR_COLORS = ['#2b2230', '#3b2a20', '#7a4a24', '#c98a3c', '#e6d3a3', '#b8b8c0', '#9a3b3b', '#3b6fb0'];
const pick = <T,>(a: readonly T[]) => a[Math.floor(Math.random() * a.length)];
function randomDoll(): Doll {
  const d = Object.fromEntries(LOOK.map(([k, opts]) => [k, pick(opts)]));
  return { ...d, head: Math.random() < 0.15 ? 'robot' : 'human', skin: Math.floor(Math.random() * SKIN.length), hairColor: pick(HAIR_COLORS) } as Doll;
}

const description = ref('');
const tools = ref<string[]>(TOOLS.map((t) => t.id));
const draft = ref<CharacterDraft | null>(null);
const busy = ref(false);
const error = ref('');

// Every model some character already offers, as the server allows.
const models = computed(() => [...new Set(props.chars.flatMap((c) => [c.model, ...(c.models ?? [])]))]);

function toggleTool(list: string[], id: string) {
  return list.includes(id) ? list.filter((x) => x !== id) : [...list, id];
}

async function run(fn: () => Promise<void>) {
  busy.value = true;
  error.value = '';
  try {
    await fn();
  } catch (err) {
    error.value = (err as Error).message;
  } finally {
    busy.value = false;
  }
}

const generate = () => run(async () => {
  const d = await api<CharacterDraft>('/api/characters/draft', {
    method: 'POST',
    body: JSON.stringify({ description: description.value.trim(), tools: tools.value }),
  });
  draft.value = { ...d, doll: normalizeDoll(d.doll) }; // the selects need every trait set
});

const save = () => run(async () => {
  const c = await api<Character>('/api/characters', { method: 'POST', body: JSON.stringify(draft.value) });
  draft.value = null;
  description.value = '';
  emit('created', c);
});
</script>

<template>
  <form
    class="room-form char-form"
    @submit.prevent="draft ? save() : generate()"
  >
    <h3>New character <span class="muted">· describe it, then review the draft</span></h3>

    <template v-if="!draft">
      <textarea
        v-model="description"
        rows="3"
        maxlength="1000"
        aria-label="Character description"
        placeholder="A retired sea captain who explains economics through storms and cargo"
      />
      <div class="room-form-row form-foot">
        <div
          class="member-chips"
          role="group"
          aria-label="Tools"
        >
          <span class="chips-label">Tools</span>
          <button
            v-for="t in TOOLS"
            :key="t.id"
            type="button"
            class="member-chip"
            :aria-pressed="tools.includes(t.id)"
            @click="tools = toggleTool(tools, t.id)"
          >
            {{ t.label }}
          </button>
        </div>
        <button
          class="room-create"
          type="submit"
          :disabled="busy || !description.trim()"
        >
          {{ busy ? 'Drafting…' : 'Generate' }}
        </button>
      </div>
    </template>

    <template v-else>
      <div class="char-row">
        <label>Name <input
          v-model="draft.name"
          type="text"
          maxlength="40"
          required
        ></label>
        <label class="char-color">Color <input
          v-model="draft.accentColor"
          type="color"
        ></label>
      </div>
      <div class="char-look">
        <img
          class="char-doll"
          :src="dollURL(draft.doll, 'idle', draft.accentColor)"
          alt="Portrait preview"
        >
        <div class="char-look-side">
          <button
            type="button"
            class="member-chip"
            @click="draft.doll = randomDoll()"
          >
            Randomize look
          </button>
          <details>
            <summary>Adjust look</summary>
            <div class="char-look-fields">
              <label
                v-for="[k, opts] in LOOK"
                :key="k"
              >{{ LOOK_LABELS[k] }} <select v-model="draft.doll[k]">
                <option
                  v-for="o in opts"
                  :key="o"
                  :value="o"
                >{{ o }}</option>
              </select></label>
              <label>Skin <select v-model.number="draft.doll.skin">
                <option
                  v-for="(_, i) in SKIN"
                  :key="i"
                  :value="i"
                >{{ SKIN_LABELS[i] }}</option>
              </select></label>
              <label class="char-color">Hair <input
                v-model="draft.doll.hairColor"
                type="color"
              ></label>
            </div>
          </details>
        </div>
      </div>
      <label>Tagline <input
        v-model="draft.tagline"
        type="text"
        maxlength="80"
      ></label>
      <label>Card description <textarea
        v-model="draft.description"
        rows="2"
        maxlength="300"
      /></label>
      <label>Greeting <input
        v-model="draft.selectLine"
        type="text"
        maxlength="160"
      ></label>
      <label>Persona <span class="muted">· memory instructions are added for you</span>
        <textarea
          v-model="draft.persona"
          rows="7"
          maxlength="4000"
          required
        />
      </label>
      <div
        class="member-chips"
        role="group"
        aria-label="Tools"
      >
        <button
          v-for="t in TOOLS"
          :key="t.id"
          type="button"
          class="member-chip"
          :aria-pressed="draft.tools.includes(t.id)"
          @click="draft.tools = toggleTool(draft.tools, t.id)"
        >
          {{ t.label }}
        </button>
      </div>
      <div class="char-row">
        <label>Model <select v-model="draft.model">
          <option
            v-for="m in models"
            :key="m"
            :value="m"
          >{{ m }}</option>
        </select></label>
        <label>Discretion {{ draft.discretion }}/10
          <input
            v-model.number="draft.discretion"
            type="range"
            min="0"
            max="10"
          >
        </label>
      </div>
      <div class="room-form-row">
        <button
          type="button"
          class="member-chip"
          :disabled="busy"
          @click="draft = null"
        >
          Discard
        </button>
        <button
          class="room-create"
          type="submit"
          :disabled="busy"
        >
          {{ busy ? 'Saving…' : 'Save character' }}
        </button>
      </div>
    </template>

    <p
      v-if="error"
      class="notice"
    >
      {{ error }}
    </p>
  </form>
</template>
