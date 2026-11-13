<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue';

const emit = defineEmits<{ start: [] }>();
function onKey(e: KeyboardEvent) {
  if ((e.key !== 'Enter' && e.key !== ' ') || (e.target as Element).closest('.hud')) return;
  e.preventDefault();
  emit('start');
}
onMounted(() => window.addEventListener('keydown', onKey));
onUnmounted(() => window.removeEventListener('keydown', onKey));
</script>

<template>
  <section
    class="title-screen"
    @click="emit('start')"
  >
    <p class="kicker">
      A conversation game
    </p>
    <h1 class="logo">
      CHARACTERS
    </h1>
    <button
      class="press-start"
      autofocus
    >
      PRESS START
    </button>
  </section>
</template>
