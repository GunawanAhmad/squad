<script setup lang="ts">
import { computed, ref } from 'vue';
import { dollURL } from '../paperDoll';
import type { Character, PortraitState } from '../types';

const props = withDefaults(defineProps<{ character: Character; state?: PortraitState }>(), { state: 'idle' });
const failed = ref(''); // painted portrait that failed to load; draw the doll instead
const src = computed(() => {
  const painted = props.character.ui.portrait?.[props.state];
  return painted && failed.value !== painted
    ? painted
    : dollURL(props.character.ui.doll, props.state, props.character.ui.accentColor);
});
</script>

<template>
  <div
    class="portrait"
    :class="state"
    :style="{ '--accent': character.ui.accentColor }"
  >
    <img
      :src="src"
      :alt="character.name"
      draggable="false"
      @error="failed = src"
    >
  </div>
</template>
