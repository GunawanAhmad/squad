<script setup lang="ts">
import { onMounted, ref } from 'vue';
import CharacterPortrait from './CharacterPortrait.vue';
import type { Character, Room } from '../types';

const props = defineProps<{ chars: Character[] }>();
const emit = defineEmits<{ pick: [Character]; openRoom: [Room] }>();

const rooms = ref<Room[]>([]);
const name = ref('');
const selected = ref<string[]>([]);
const creating = ref(false);
const error = ref('');

onMounted(async () => {
  try {
    rooms.value = await (await fetch('/api/rooms')).json();
  } catch {
    // The character cards still work; the rooms list can be retried on reload.
  }
});

function toggle(id: string) {
  selected.value = selected.value.includes(id)
    ? selected.value.filter((x) => x !== id)
    : [...selected.value, id];
}

async function createRoom() {
  if (selected.value.length < 2 || !name.value.trim()) {
    error.value = 'Give the room a name and pick at least two characters.';
    return;
  }
  creating.value = true;
  error.value = '';
  try {
    const res = await fetch('/api/rooms', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: name.value.trim(), members: selected.value }),
    });
    if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
    const room = (await res.json()) as Room;
    rooms.value.unshift(room);
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

    <section class="rooms">
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
      <p
        v-else
        class="muted"
      >
        No rooms yet.
      </p>

      <form
        class="room-form"
        @submit.prevent="createRoom"
      >
        <h3>New room</h3>
        <label>
          Name
          <input
            v-model="name"
            type="text"
            maxlength="60"
            placeholder="office"
            required
          >
        </label>
        <fieldset>
          <legend>Members (at least two)</legend>
          <label
            v-for="c in chars"
            :key="c.id"
            class="member-pick"
          >
            <input
              type="checkbox"
              :value="c.id"
              :checked="selected.includes(c.id)"
              @change="toggle(c.id)"
            >
            {{ c.name }}
          </label>
        </fieldset>
        <p
          v-if="error"
          class="notice"
        >
          {{ error }}
        </p>
        <button
          class="room-create"
          type="submit"
          :disabled="creating || selected.length < 2 || !name.trim()"
        >
          {{ creating ? 'Creating…' : 'Create room' }}
        </button>
      </form>
    </section>
  </section>
</template>
