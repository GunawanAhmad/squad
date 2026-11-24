<script setup lang="ts">
import { computed, onMounted, onUpdated, ref } from 'vue';
import { highlight, renderMarkdown } from '../markdown';
import type { Character, Message, ToolCall } from '../types';

const props = defineProps<{ message: Message; character: Character }>();
const emit = defineEmits<{ undo: [toolCallId: number] }>();
const root = ref<HTMLElement | null>(null);

const text = computed(() => {
  const m = props.message;
  return m.content || (m.role === 'assistant' && !m.tools?.length ? '…' : '');
});
const html = computed(() => (props.message.role === 'assistant' ? renderMarkdown(text.value) : null));

onMounted(() => highlight(root.value));
onUpdated(() => highlight(root.value));

// Write tools carry the tool_calls row id while streaming, so their
// confirmation can offer the same undo the chat command does.
const WRITES = ['plan_tasks', 'update_task', 'complete_task', 'log_activity', 'remind_me', 'cancel_reminder', 'skip_checkin'];

function toolLabel(t: ToolCall) {
  let a: Record<string, unknown> = {};
  try { a = JSON.parse(t.args); } catch {}
  switch (t.name) {
    case 'web_search': return `🔎 searched: ${a.query ?? ''}`;
    case 'fetch_url': return `📄 read: ${a.url ?? ''}`;
    case 'plan_tasks': return '✓ planned';
    case 'update_task': return `✎ updated ${a.task ?? 'a plan'}`;
    case 'complete_task': return `✓ ${a.status === 'dropped' ? 'dropped' : 'completed'} ${a.task ?? 'a plan'}`;
    case 'log_activity': return `⏱ logged ${a.description ?? ''}`;
    case 'query_activities': return '📋 looked up the log';
    case 'remind_me': return `⏰ reminder: ${a.text ?? ''}`;
    case 'list_reminders': return '📋 looked up reminders';
    case 'list_tasks': return '📋 looked up plans';
    case 'cancel_reminder': return '✕ reminder cancelled';
    case 'skip_checkin': return '– skipped a check-in';
    case 'remember': return '🧠 remembered';
    case 'forget': return '🗑 forget';
    case 'recall': return '🔍 searched the past';
    case 'undo': return '↩ undone';
    default: return `🔧 ${t.name}`;
  }
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
    >
      {{ toolLabel(t) }}
      <button
        v-if="t.id && WRITES.includes(t.name)"
        class="undo-link"
        title="Undo this write"
        @click="emit('undo', t.id!)"
      >
        undo
      </button>
    </span>
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
  </div>
</template>
