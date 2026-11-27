<script setup lang="ts">
import { computed, ref } from 'vue';
import { api } from '../api';
import CharacterPortrait from './CharacterPortrait.vue';
import NewCharacterForm from './NewCharacterForm.vue';
import type { Character, Room } from '../types';

const props = defineProps<{ chars: Character[]; rooms: Room[] }>();
const emit = defineEmits<{ pick: [Character]; openRoom: [Room]; created: [Character] }>();

const name = ref('');
const selected = ref<string[]>([]);
const creating = ref(false);
const roomDialog = ref<HTMLDialogElement>();
const charDialog = ref<HTMLDialogElement>();
const error = ref('');

function toggle(id: string) {
  selected.value = selected.value.includes(id)
    ? selected.value.filter((x) => x !== id)
    : [...selected.value, id];
}

// Clicks on the dialog itself (not its form) land on the backdrop.
function closeOnBackdrop(e: MouseEvent) {
  if (e.target === e.currentTarget) (e.currentTarget as HTMLDialogElement).close();
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
    roomDialog.value?.close();
    emit('openRoom', room);
  } catch (err) {
    error.value = (err as Error).message;
  } finally {
    creating.value = false;
  }
}

function members(room: Room) {
  return room.members.map((id) => {
    const c = props.chars.find((x) => x.id === id);
    return { id, name: c?.name ?? id, color: c?.ui.accentColor };
  });
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
      <button
        class="home-card home-new"
        @click="charDialog?.showModal()"
      >
        <span class="home-new-plus">+</span>
        New character
      </button>
    </div>

    <div class="rooms">
      <h2>Rooms</h2>
      <p class="muted rooms-hint">
        Everyone in a room hears everything said there. You are always in it.
      </p>
      <ul class="room-list">
        <li
          v-for="r in rooms"
          :key="r.id"
        >
          <button
            class="room-card"
            @click="emit('openRoom', r)"
          >
            <span class="room-name">{{ r.name }}</span>
            <span class="room-members">
              <span
                v-for="m in members(r)"
                :key="m.id"
                class="room-member"
                :style="{ '--accent': m.color }"
              >{{ m.name }}</span>
            </span>
          </button>
        </li>
        <li>
          <button
            class="room-card room-new"
            @click="roomDialog?.showModal()"
          >
            + New room
          </button>
        </li>
      </ul>
    </div>

    <dialog
      ref="roomDialog"
      class="form-dialog"
      @click="closeOnBackdrop"
    >
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
      <button
        type="button"
        class="dialog-close"
        aria-label="Close"
        @click="roomDialog?.close()"
      >
        ×
      </button>
    </dialog>

    <dialog
      ref="charDialog"
      class="form-dialog"
      @click="closeOnBackdrop"
    >
      <NewCharacterForm
        :chars="chars"
        @created="charDialog?.close(); emit('created', $event)"
      />
      <button
        type="button"
        class="dialog-close"
        aria-label="Close"
        @click="charDialog?.close()"
      >
        ×
      </button>
    </dialog>
  </section>
</template>
