<script setup lang="ts">
defineProps<{ options: string[] }>();
const model = defineModel<string>();
</script>

<template>
  <select
    v-model="model"
    class="game-select"
  >
    <option
      v-for="o in options"
      :key="o"
      :value="o"
    >
      {{ o }}
    </option>
  </select>
</template>
