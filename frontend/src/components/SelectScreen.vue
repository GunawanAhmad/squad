<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue';
import { play, speak } from '../audio';
import CharacterPortrait from './CharacterPortrait.vue';
import type { Character } from '../types';

const props = defineProps<{ chars: Character[]; initial: Character | null }>();
const emit = defineEmits<{ pick: [Character]; back: [] }>();

const idx = ref(Math.max(0, props.chars.findIndex((c) => c.id === props.initial?.id)));
const dir = ref(1); // slide direction for the showcase swap
const locked = ref(false);
const cards: HTMLButtonElement[] = []; // by index; v-for template-ref arrays don't guarantee order
const hi = computed(() => props.chars[idx.value]);

function highlight(i: number) {
  if (locked.value || i === idx.value) return;
  dir.value = i > idx.value ? 1 : -1;
  idx.value = i;
  play('tick');
  nextTick(() => cards[i]?.focus());
}

let finish: (() => void) | null = null;
async function confirm() {
  if (locked.value) return finish?.(); // a second press skips the lock-in sequence
  locked.value = true;
  const c = hi.value;
  play('lock');
  let done = false;
  finish = () => { if (!done) { done = true; emit('pick', c); } };
  const minShow = new Promise((r) => setTimeout(r, 900));
  const maxWait = new Promise((r) => setTimeout(r, 5000)); // SpeechSynthesis onend is not always reliable
  await Promise.all([minShow, Promise.race([speak(c, c.ui.selectLine), maxWait])]);
  finish();
}

// Touch has no hover: first tap highlights, second confirms. With a mouse, hover already highlighted it.
const onCard = (i: number) => (locked.value || i === idx.value ? confirm() : highlight(i));

function onKey(e: KeyboardEvent) {
  if ((e.target as Element).closest('.hud')) return;
  const n = props.chars.length;
  if (locked.value) {
    if (e.key === 'Enter' || e.key === ' ' || e.key === 'Escape') { e.preventDefault(); finish?.(); }
    return;
  }
  if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') highlight((idx.value - 1 + n) % n);
  else if (e.key === 'ArrowRight' || e.key === 'ArrowDown') highlight((idx.value + 1) % n);
  else if (e.key === 'Enter') confirm(); // Space is left to the focused card's native click
  else if (e.key === 'Escape') { play('back'); emit('back'); } else return;
  e.preventDefault();
}

onMounted(() => {
  window.addEventListener('keydown', onKey);
  cards[idx.value]?.focus();
});
onUnmounted(() => window.removeEventListener('keydown', onKey));
</script>

<template>
  <section
    v-if="hi"
    class="select-screen"
    :class="{ locked }"
    :style="{ '--accent': hi.ui.accentColor, '--dir': dir }"
    @click="locked && finish?.()"
  >
    <h2 class="screen-title">
      SELECT YOUR CHARACTER
    </h2>

    <div class="showcase">
      <Transition name="swap">
        <div
          :key="hi.id"
          class="showcase-inner"
        >
          <CharacterPortrait
            :character="hi"
            class="showcase-portrait"
          />
          <div class="info">
            <div class="nameplate">
              {{ hi.name }}
            </div>
            <p class="tagline">
              {{ hi.ui.tagline }}
            </p>
            <p class="description">
              {{ hi.ui.description }}
            </p>
            <ul class="stats">
              <li
                v-for="(s, i) in hi.ui.stats"
                :key="s.label"
                :style="{ '--v': s.value / 10, '--i': i }"
              >
                <span>{{ s.label }}</span>
                <span class="bar"><span /></span>
                <b>{{ s.value }}</b>
              </li>
            </ul>
          </div>
        </div>
      </Transition>
      <p
        v-if="locked"
        class="select-line"
      >
        “{{ hi.ui.selectLine }}”
      </p>
    </div>

    <div
      class="roster"
      role="listbox"
      aria-label="Characters"
    >
      <button
        v-for="(c, i) in chars"
        :key="c.id"
        :ref="(el) => { cards[i] = el as HTMLButtonElement; }"
        class="card"
        role="option"
        :aria-selected="i === idx"
        :style="{ '--accent': c.ui.accentColor }"
        @pointerenter="$event.pointerType === 'mouse' && highlight(i)"
        @click.stop="onCard(i)"
      >
        <CharacterPortrait :character="c" />
        <span>{{ c.name }}</span>
      </button>
    </div>

    <p class="hint">
      ←→ choose · Enter confirm · Esc back
    </p>
    <div
      v-if="locked"
      class="flash"
    />
  </section>
</template>
