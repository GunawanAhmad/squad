<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { api } from './api';
import { play, settings, unlock } from './audio';
import CharacterPortrait from './components/CharacterPortrait.vue';
import ChatScreen from './components/ChatScreen.vue';
import HomeScreen from './components/HomeScreen.vue';
import MemoryScreen from './components/MemoryScreen.vue';
import RoomScreen from './components/RoomScreen.vue';
import type { Character, Room } from './types';

type Screen = 'home' | 'chat' | 'memory' | 'room';
const screen = ref<Screen>('home');
const chars = ref<Character[]>([]);
const current = ref<Character | null>(null);
const currentRoom = ref<Room | null>(null);
const rooms = ref<Room[]>([]);
let beforeMemory: Screen = 'home';

onMounted(async () => {
  // Browsers only allow audio after a user gesture; the first one anywhere unlocks it.
  window.addEventListener('pointerdown', unlock, { once: true });
  window.addEventListener('keydown', unlock, { once: true });
  chars.value = await api<Character[]>('/api/characters');
  try {
    rooms.value = await api<Room[]>('/api/rooms');
  } catch {
    // The rail still works without the rooms list; a reload retries it.
  }
});

function openRoom(r: Room) {
  play(screen.value === 'home' ? 'lock' : 'tick');
  if (!rooms.value.some((x) => x.id === r.id)) rooms.value.unshift(r);
  currentRoom.value = r;
  screen.value = 'room';
}

function pick(c: Character) {
  if (screen.value === 'chat' && current.value?.id === c.id) return;
  play(screen.value === 'home' ? 'lock' : 'tick');
  current.value = c;
  screen.value = 'chat';
}

function created(c: Character) {
  chars.value.push(c);
  pick(c);
}

function home() {
  play('back');
  screen.value = 'home';
}

function toggleMemory() {
  if (screen.value === 'memory') return void (screen.value = beforeMemory);
  beforeMemory = screen.value;
  screen.value = 'memory';
}
</script>

<template>
  <div
    class="shell"
    :class="{ 'has-rail': screen !== 'home' }"
  >
    <nav
      v-if="screen !== 'home'"
      class="rail"
      aria-label="Characters"
    >
      <button
        class="rail-btn"
        aria-label="All characters"
        title="All characters"
        @click="home"
      >
        <svg
          width="20"
          height="20"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.8"
          stroke-linejoin="round"
          aria-hidden="true"
        ><rect
          x="3"
          y="3"
          width="7"
          height="7"
          rx="1.5"
        /><rect
          x="14"
          y="3"
          width="7"
          height="7"
          rx="1.5"
        /><rect
          x="3"
          y="14"
          width="7"
          height="7"
          rx="1.5"
        /><rect
          x="14"
          y="14"
          width="7"
          height="7"
          rx="1.5"
        /></svg>
      </button>
      <span class="rail-sep" />
      <button
        v-for="c in chars"
        :key="c.id"
        class="rail-avatar"
        :aria-label="c.name"
        :title="c.name"
        :aria-current="screen === 'chat' && current?.id === c.id"
        :style="{ '--accent': c.ui.accentColor }"
        @click="pick(c)"
      >
        <CharacterPortrait
          :character="c"
          class="mini"
        />
      </button>
      <template v-if="rooms.length">
        <span class="rail-sep" />
        <button
          v-for="r in rooms"
          :key="r.id"
          class="rail-room"
          :aria-label="r.name"
          :title="r.name"
          :aria-current="screen === 'room' && currentRoom?.id === r.id"
          @click="openRoom(r)"
        >
          {{ r.name.slice(0, 2) }}
        </button>
      </template>
    </nav>

    <Transition
      name="screen"
      mode="out-in"
    >
      <HomeScreen
        v-if="screen === 'home'"
        :chars="chars"
        :rooms="rooms"
        @pick="pick"
        @open-room="openRoom"
        @created="created"
      />
      <ChatScreen
        v-else-if="screen === 'chat' && current"
        :key="current.id"
        :character="current"
        @back="home"
      />
      <RoomScreen
        v-else-if="screen === 'room' && currentRoom"
        :key="currentRoom.id"
        :room="currentRoom"
        :chars="chars"
        @back="home"
      />
      <MemoryScreen
        v-else-if="screen === 'memory'"
        @back="toggleMemory"
      />
    </Transition>

    <div class="tools">
      <button
        class="rail-btn"
        :aria-pressed="screen === 'memory'"
        aria-label="Memory"
        title="Memory"
        @click="toggleMemory"
      >
        <svg
          width="22"
          height="22"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.8"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        ><path d="M2 4h7a3 3 0 0 1 3 3v13a2 2 0 0 0-2-2H2z" /><path d="M22 4h-7a3 3 0 0 0-3 3v13a2 2 0 0 1 2-2h8z" /></svg>
      </button>
      <details class="settings">
        <summary
          class="rail-btn"
          aria-label="Sound settings"
          title="Sound"
        >
          <svg
            width="22"
            height="22"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="1.8"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          ><path d="M11 5 6 9H2v6h4l5 4z" /><path
            v-if="!settings.muted"
            d="M15.5 8.5a5 5 0 0 1 0 7M19 5a10 10 0 0 1 0 14"
          /><path
            v-else
            d="m16 9 6 6M22 9l-6 6"
          /></svg>
        </summary>
        <div class="settings-pop">
          <label><input
            v-model="settings.muted"
            type="checkbox"
          > Mute</label>
          <label>Volume
            <input
              v-model.number="settings.volume"
              type="range"
              min="0"
              max="1"
              step="0.05"
              :disabled="settings.muted"
            >
          </label>
        </div>
      </details>
    </div>
  </div>
</template>
