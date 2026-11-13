<script setup lang="ts">
import { computed, ref } from 'vue';
import type { Character, PortraitState } from '../types';

const props = withDefaults(defineProps<{ character: Character; state?: PortraitState }>(), { state: 'idle' });
const src = computed(() => props.character.ui.portrait[props.state]);
const failed = ref(''); // src that failed to load; show the generated placeholder instead
const initials = computed(() => props.character.name.split(/\s+/).map((w) => w[0]).join('').slice(0, 2).toUpperCase());
</script>

<template>
  <div
    class="portrait"
    :class="state"
    :style="{ '--accent': character.ui.accentColor }"
  >
    <img
      v-if="failed !== src"
      :src="src"
      :alt="character.name"
      draggable="false"
      @error="failed = src"
    >
    <div
      v-else
      class="placeholder"
      role="img"
      :aria-label="character.name"
    >
      {{ initials }}
    </div>
  </div>
</template>
