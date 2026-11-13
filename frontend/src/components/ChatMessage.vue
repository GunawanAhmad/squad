<script setup lang="ts">
import { computed, onMounted, onUpdated, ref } from 'vue';
import { speak } from '../audio';
import { highlight, renderMarkdown } from '../markdown';
import type { Character, Message, ToolCall } from '../types';

const props = defineProps<{ message: Message; character: Character }>();
const root = ref<HTMLElement | null>(null);

const text = computed(() => {
  const m = props.message;
  return m.content || (m.role === 'assistant' && !m.tools?.length ? '…' : '');
});
const html = computed(() => (props.message.role === 'assistant' ? renderMarkdown(text.value) : null));

onMounted(() => highlight(root.value));
onUpdated(() => highlight(root.value));

// Spoken text: the rendered reply minus KaTeX's hidden MathML copy, so formulas aren't read twice.
function readAloud() {
  const t = root.value?.querySelector('.text')?.cloneNode(true) as HTMLElement | undefined;
  t?.querySelectorAll('.katex-mathml').forEach((e) => e.remove());
  speak(props.character, t?.textContent ?? '');
}

function toolLabel(t: ToolCall) {
  let a: { query?: string; url?: string } = {};
  try { a = JSON.parse(t.args); } catch {}
  if (t.name === 'web_search') return `🔎 searched: ${a.query ?? ''}`;
  if (t.name === 'fetch_url') return `📄 read: ${a.url ?? ''}`;
  return `🔧 ${t.name}`;
}
</script>

<template>
  <div
    ref="root"
    class="msg"
    :class="message.role"
  >
    <span
      v-for="(t, i) in message.tools ?? []"
      :key="i"
      class="tool"
    >{{ toolLabel(t) }}</span>
    <!-- eslint-disable vue/no-v-html -- sanitized by DOMPurify in markdown.js -->
    <div
      v-if="html !== null"
      class="text"
      v-html="html"
    />
    <!-- eslint-enable vue/no-v-html -->
    <!-- v-text, not {{ }}: user text is pre-wrap, so template whitespace would show -->
    <div
      v-else
      class="text"
      v-text="text"
    />
    <button
      v-if="html !== null && message.content"
      class="read-aloud"
      title="Read aloud"
      aria-label="Read aloud"
      @click="readAloud"
    >
      🔊
    </button>
  </div>
</template>
