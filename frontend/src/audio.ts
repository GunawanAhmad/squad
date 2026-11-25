import { reactive, watch } from 'vue';

export const settings = reactive({ muted: false, volume: 0.6 });
try { Object.assign(settings, JSON.parse(localStorage.getItem('audio') ?? '{}')); } catch {}
watch(settings, () => {
  try { localStorage.setItem('audio', JSON.stringify(settings)); } catch {}
  if (out) out.gain.value = settings.muted ? 0 : settings.volume;
});

type Sfx = 'tick' | 'lock' | 'back' | 'arrive';
// Synth fallback when /sounds/<name>.mp3 is missing: [start Hz, end Hz, seconds].
const synth: Record<Sfx, [number, number, number]> = {
  tick: [1400, 1400, 0.03], lock: [440, 1320, 0.3], back: [700, 300, 0.12], arrive: [660, 990, 0.1],
};
const buffers: Partial<Record<Sfx, AudioBuffer>> = {};
let ctx: AudioContext | null = null;
let out: GainNode | null = null;

// Browsers block audio until a user gesture; App calls this on the first click or key.
export function unlock() {
  if (ctx) return void ctx.resume();
  ctx = new AudioContext();
  out = ctx.createGain();
  out.gain.value = settings.muted ? 0 : settings.volume;
  out.connect(ctx.destination);
  for (const name of Object.keys(synth) as Sfx[]) {
    fetch(`/sounds/${name}.mp3`)
      .then((r) => r.arrayBuffer())
      .then((b) => ctx!.decodeAudioData(b))
      .then((b) => { buffers[name] = b; })
      .catch(() => {}); // play() synthesizes instead
  }
}

export function play(name: Sfx) {
  if (!ctx || !out || settings.muted) return;
  const b = buffers[name];
  if (!b) return tone(...synth[name], 'triangle', 0.3);
  const s = ctx.createBufferSource();
  s.buffer = b;
  s.connect(out);
  s.start();
}

function tone(f0: number, f1: number, dur: number, type: OscillatorType, vol: number) {
  const t = ctx!.currentTime, o = ctx!.createOscillator(), g = ctx!.createGain();
  o.type = type;
  o.frequency.setValueAtTime(f0, t);
  o.frequency.exponentialRampToValueAtTime(f1, t + dur);
  g.gain.setValueAtTime(vol, t);
  g.gain.exponentialRampToValueAtTime(0.001, t + dur);
  o.connect(g).connect(out!);
  o.start(t);
  o.stop(t + dur);
}

// Text blip; call it on every streamed chunk, it rate-limits itself so fast streams don't turn into a buzz.
let lastBlip = 0;
export function blip(freq: number) {
  if (!ctx || settings.muted || performance.now() - lastBlip < 70) return;
  lastBlip = performance.now();
  tone(freq * (0.94 + Math.random() * 0.12), freq, 0.045, 'square', 0.06);
}
