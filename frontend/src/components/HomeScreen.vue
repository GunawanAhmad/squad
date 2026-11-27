<script setup lang="ts">
import { computed, ref } from 'vue';
import { api } from '../api';
import CharacterPortrait from './CharacterPortrait.vue';
import type { Character, Room } from '../types';

const props = defineProps<{ chars: Character[]; rooms: Room[] }>();
const emit = defineEmits<{ pick: [Character]; openRoom: [Room] }>();

const name = ref('');
const selected = ref<string[]>([]);
const creating = ref(false);
const error = ref('');

function toggle(id: string) {
  selected.value = selected.value.includes(id)
    ? selected.value.filter((x) => x !== id)
    : [...selected.value, id];
}

// Used when the name is left blank: "Ria, Bob, Claude".
const defaultName = computed(() =>
  props.chars
    .filter((c) => selected.value.includes(c.id))
    .map((c) => c.name)
    .join(', ')
    .slice(0, 60),
);

async function createRoom() {
  if (selected.value.length < 2) return;
  creating.value = true;
  error.value = '';
  try {
    const room = await api<Room>('/api/rooms', {
      method: 'POST',
      body: JSON.stringify({ name: name.value.trim() || defaultName.value, members: selected.value }),
    });
    name.value = '';
    selected.value = [];
    emit('openRoom', room);
  } catch (err) {
    error.value = (err as Error).message;
  } finally {
    creating.value = false;
  }
}

function memberNames(room: Room) {
  return room.members
    .map((id) => props.chars.find((c) => c.id === id)?.name ?? id)
    .join(' · ');
}
</script>

<template>
  <section class="home-screen">
    <div class="home-head">
      <p class="wordmark">
        SQUAD
      </p>
      <h1>Who do you want to talk to?</h1>
    </div>
    <div class="home-grid">
      <button
        v-for="c in chars"
        :key="c.id"
        class="home-card"
        :style="{ '--accent': c.ui.accentColor }"
        @click="emit('pick', c)"
      >
        <CharacterPortrait
          :character="c"
          class="home-art"
        />
        <span class="home-info">
          <span class="home-name">{{ c.name }}</span>
          <span class="home-tagline">{{ c.ui.tagline }}</span>
          <span class="home-desc">{{ c.ui.description }}</span>
        </span>
      </button>
    </div>

    <div class="rooms">
      <h2>Rooms</h2>
      <p class="muted rooms-hint">
        Everyone in a room hears everything said there. You are always in it.
      </p>
      <ul
        v-if="rooms.length"
        class="room-list"
      >
        <li
          v-for="r in rooms"
          :key="r.id"
        >
          <button
            class="room-card"
            @click="emit('openRoom', r)"
          >
            <span class="room-name">{{ r.name }}</span>
            <span class="muted">{{ memberNames(r) }}</span>
          </button>
        </li>
      </ul>

      <form
        class="room-form"
        @submit.prevent="createRoom"
      >
        <h3>New room <span class="muted">· pick two or more</span></h3>
        <div
          class="member-chips"
          role="group"
          aria-label="Room members"
        >
          <button
            v-for="c in chars"
            :key="c.id"
            type="button"
            class="member-chip"
            :style="{ '--accent': c.ui.accentColor }"
            :aria-pressed="selected.includes(c.id)"
            @click="toggle(c.id)"
          >
            {{ c.name }}
          </button>
        </div>
        <div class="room-form-row">
          <input
            v-model="name"
            type="text"
            maxlength="60"
            aria-label="Room name"
            :placeholder="defaultName || 'Room name (optional)'"
          >
          <button
            class="room-create"
            type="submit"
            :disabled="creating || selected.length < 2"
          >
            {{ creating ? 'Creating…' : 'Create' }}
          </button>
        </div>
        <p
          v-if="error"
          class="notice"
        >
          {{ error }}
        </p>
      </form>
    </div>
  </section>
</template>
