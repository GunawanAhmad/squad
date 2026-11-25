<script setup lang="ts">
import CharacterPortrait from './CharacterPortrait.vue';
import type { Character } from '../types';

defineProps<{ chars: Character[] }>();
const emit = defineEmits<{ pick: [Character] }>();
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
  </section>
</template>
