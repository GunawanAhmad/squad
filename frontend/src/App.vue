<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { play, settings, unlock } from './audio';
import ChatScreen from './components/ChatScreen.vue';
import MemoryScreen from './components/MemoryScreen.vue';
import SelectScreen from './components/SelectScreen.vue';
import TitleScreen from './components/TitleScreen.vue';
import type { Character } from './types';

const screen = ref<'title' | 'select' | 'chat' | 'memory'>('title');
const chars = ref<Character[]>([]);
const current = ref<Character | null>(null);

onMounted(async () => {
  chars.value = await (await fetch('/api/characters')).json();
});

function start() {
  unlock();
  play('lock');
  screen.value = 'select';
}

function pick(c: Character) {
  current.value = c;
  screen.value = 'chat';
}
</script>

<template>
  <Transition
    name="screen"
    mode="out-in"
  >
    <TitleScreen
      v-if="screen === 'title'"
      @start="start"
    />
    <SelectScreen
      v-else-if="screen === 'select'"
      :chars="chars"
      :initial="current"
      @pick="pick"
      @back="screen = 'title'"
    />
    <ChatScreen
      v-else-if="screen === 'chat' && current"
      :key="current.id"
      :character="current"
      @back="screen = 'select'"
    />
    <MemoryScreen
      v-else-if="screen === 'memory'"
      @back="screen = 'select'"
    />
  </Transition>

  <div class="hud">
    <button
      :aria-label="settings.muted ? 'Unmute' : 'Mute'"
      :title="settings.muted ? 'Unmute' : 'Mute'"
      @click="settings.muted = !settings.muted"
    >
      {{ settings.muted ? '🔇' : '🔊' }}
    </button>
    <input
      v-model.number="settings.volume"
      type="range"
      min="0"
      max="1"
      step="0.05"
      aria-label="Volume"
      :disabled="settings.muted"
    >
    <button
      v-if="screen === 'select' || screen === 'memory'"
      aria-label="Memory"
      title="Memory"
      @click="screen = screen === 'memory' ? 'select' : 'memory'"
    >
      🧠
    </button>
  </div>
</template>
