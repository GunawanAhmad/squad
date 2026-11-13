<script setup lang="ts">
defineProps<{ variant?: 'primary' }>();
</script>

<template>
  <button
    class="game-btn"
    :class="variant"
  >
    <slot />
  </button>
</template>
