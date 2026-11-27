// Character portraits drawn in code from a few traits (ui.doll), for characters
// without a painted portrait. The trait lists mirror dollOptions in handlers_characters.go.
import type { PortraitState } from './types';

export const SKIN = ['#f6d7c3', '#eabf9f', '#d49a6a', '#a86b45', '#7a4a2e', '#4f2f1d'];
export const DOLL_OPTIONS = {
  head: ['human', 'robot'],
  build: ['average', 'slim', 'broad'],
  hair: ['short', 'none', 'bob', 'long', 'ponytail', 'twintails', 'curly', 'spiky', 'bun'],
  eyes: ['round', 'sleepy', 'sharp'],
  facialHair: ['none', 'stubble', 'mustache', 'beard'],
  outfit: ['tunic', 'jacket', 'robe', 'armor', 'hoodie'],
  accessory: ['none', 'glasses', 'headphones', 'hat', 'scarf'],
} as const;
type Opt = typeof DOLL_OPTIONS;
export type Doll = { [K in keyof Opt]: Opt[K][number] } & { skin: number; hairColor: string };

const HEX = /^#[0-9a-f]{6}$/i;

// Traits come from a model or a hand-edited file: keep known values, default the rest (the first option).
export function normalizeDoll(raw: unknown): Doll {
  const r = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>;
  const d = { skin: 1, hairColor: '#3b2a20' } as Doll;
  for (const k of Object.keys(DOLL_OPTIONS) as (keyof Opt)[]) {
    const opts: readonly string[] = DOLL_OPTIONS[k];
    (d as Record<string, unknown>)[k] = opts.includes(r[k] as string) ? r[k] : opts[0];
  }
  if (Number.isInteger(r.skin) && (r.skin as number) >= 0 && (r.skin as number) < SKIN.length) d.skin = r.skin as number;
  if (typeof r.hairColor === 'string' && HEX.test(r.hairColor)) d.hairColor = r.hairColor;
  return d;
}

function mix(a: string, b: string, t: number) {
  const p = (h: string) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
  const x = p(a), y = p(b);
  return '#' + x.map((v, i) => Math.round(v + (y[i] - v) * t).toString(16).padStart(2, '0')).join('');
}
const dark = (c: string, t: number) => mix(c, '#000000', t);
const light = (c: string, t: number) => mix(c, '#ffffff', t);

export function renderDoll(raw: unknown, state: PortraitState, accent: string): string {
  const d = normalizeDoll(raw);
  const A = HEX.test(accent) ? accent : '#888888';
  const ink = '#16161e';
  const robot = d.head === 'robot';
  const skin = robot ? '#c9cad8' : SKIN[d.skin];
  const skinSh = dark(skin, 0.18);
  const hc = d.hairColor;
  const sx = { slim: 0.88, average: 1, broad: 1.14 }[d.build];
  const L = 100 - 33 * sx, R = 100 + 33 * sx; // shoulders
  const fill = { tunic: A, jacket: '#2c2e3b', robe: light(A, 0.6), armor: '#dcdce8', hoodie: A }[d.outfit];
  const pants = d.outfit === 'armor' ? '#b9bacb' : '#2a2b36';
  const O = `stroke="${ink}" stroke-width="2" stroke-linejoin="round"`;
  const s: string[] = [];

  // Back hair, behind everything.
  const back = {
    bob: 'M68 60Q66 24 100 23Q134 24 132 60L134 98Q120 102 112 96L88 96Q80 102 66 98Z',
    long: 'M68 60Q66 22 100 22Q134 22 132 60L138 150Q120 158 112 146L110 100L90 100L88 146Q80 158 62 150Z',
    ponytail: 'M118 38Q152 46 148 100Q146 132 132 142Q138 110 130 84Q126 62 112 50Z',
    twintails: 'M72 38Q42 58 48 120Q52 162 66 178Q58 130 64 96Q68 68 82 48Z M128 38Q158 58 152 120Q148 162 134 178Q142 130 136 96Q132 68 118 48Z',
  }[d.hair as string];
  if (!robot && back) s.push(`<path d="${back}" fill="${hc}" ${O}/>`);
  if (!robot && d.hair === 'bun') s.push(`<circle cx="100" cy="22" r="13" fill="${hc}" ${O}/>`);
  if (!robot && d.hair === 'twintails') s.push(`<circle cx="74" cy="40" r="4" fill="${A}" ${O}/><circle cx="126" cy="40" r="4" fill="${A}" ${O}/>`);

  // Legs (or the robe's skirt), neck, torso.
  if (d.outfit === 'robe') {
    s.push(`<path d="M${L + 2} 196L54 300L146 300L${R - 2} 196Z" fill="${fill}" ${O}/>`,
      `<path d="M86 230L82 300M114 230L118 300" stroke="${dark(fill, 0.15)}" stroke-width="2"/>`,
      `<path d="M58 288L142 288" stroke="${A}" stroke-width="4"/>`);
  } else {
    s.push(`<path d="M70 224L72 300L98 300L100 238L102 300L128 300L130 224Z" fill="${pants}" ${O}/>`);
  }
  s.push(`<rect x="92" y="86" width="16" height="24" fill="${skin}" ${O}/>`);

  const t = [`<path d="M66 122Q66 108 82 105L118 105Q134 108 134 122L132 230L68 230Z" fill="${fill}" ${O}/>`];
  if (d.outfit === 'tunic') t.push(
    `<path d="M89 105L100 124L111 105Z" fill="${skin}" ${O}/>`,
    `<path d="M76 107L124 186L112 188L68 120Z" fill="${dark(A, 0.28)}" ${O}/>`,
    `<rect x="68" y="186" width="64" height="9" fill="${dark(A, 0.5)}" ${O}/>`);
  if (d.outfit === 'jacket') t.push(
    `<path d="M91 105L109 105L108 230L92 230Z" fill="#d9d6cf" ${O}/>`,
    `<path d="M91 105L82 152L96 132Z M109 105L118 152L104 132Z" fill="#3b3e4f" ${O}/>`,
    `<path d="M90 140L90 230M110 140L110 230" stroke="${A}" stroke-width="2"/>`,
    `<rect x="68" y="192" width="64" height="8" fill="#5a4632" ${O}/><rect x="95" y="191" width="10" height="10" rx="2" fill="${A}" ${O}/>`,
    `<rect x="72" y="206" width="14" height="12" rx="2" fill="#3b3e4f" ${O}/><rect x="114" y="206" width="14" height="12" rx="2" fill="#3b3e4f" ${O}/>`);
  if (d.outfit === 'robe') t.push(
    `<path d="M89 105L100 122L111 105Z" fill="${skin}" ${O}/>`,
    `<path d="M70 110Q104 142 132 198L132 222Q98 166 68 132Z" fill="${light(A, 0.25)}" ${O}/>`,
    `<path d="M70 132Q99 166 132 222" stroke="${A}" stroke-width="3" fill="none"/>`,
    `<path d="M68 184Q100 192 132 184" stroke="${dark(A, 0.35)}" stroke-width="4" fill="none"/>`);
  if (d.outfit === 'armor') t.push(
    `<path d="M76 110L124 110L118 160L82 160Z" fill="#eeeef6" ${O}/>`,
    `<circle cx="100" cy="133" r="7" fill="${A}" ${O} filter="url(#glow)"/>`,
    `<path d="M84 174L116 174M86 188L114 188M88 202L112 202" stroke="#9a9aae" stroke-width="2"/>`,
    `<rect x="68" y="212" width="64" height="8" fill="${A}" opacity=".85"/>`);
  if (d.outfit === 'hoodie') t.push(
    `<path d="M78 106Q100 128 122 106Q120 96 100 96Q80 96 78 106Z" fill="${dark(A, 0.22)}" ${O}/>`,
    `<path d="M95 114L94 142M105 114L106 142" stroke="${light(A, 0.7)}" stroke-width="2" stroke-linecap="round"/>`,
    `<path d="M80 180L120 180L126 214L74 214Z" fill="${dark(A, 0.14)}" ${O}/>`);
  s.push(`<g transform="translate(100 0) scale(${sx} 1) translate(-100 0)">${t.join('')}</g>`);

  if (d.accessory === 'scarf') s.push(
    `<path d="M108 110L116 156L104 158L98 114Z" fill="${dark(A, 0.3)}" ${O}/>`,
    `<path d="M82 100Q100 112 118 100L122 112Q100 126 78 112Z" fill="${dark(A, 0.15)}" ${O}/>`);

  // Head and face.
  const px = state === 'thinking' ? 2 : 0, py = state === 'thinking' ? -2 : 0;
  const blink = state === 'thinking' ? '' : ' class="blink"';
  if (robot) {
    s.push(`<path d="M100 30L100 18" stroke="${ink}" stroke-width="2.5"/><circle cx="100" cy="16" r="4" fill="${A}" ${O} filter="url(#glow)"/>`,
      `<rect x="64" y="50" width="8" height="24" rx="3" fill="${A}" ${O}/><rect x="128" y="50" width="8" height="24" rx="3" fill="${A}" ${O}/>`,
      `<rect x="70" y="28" width="60" height="68" rx="20" fill="#e6e6f0" ${O}/>`,
      `<rect x="76" y="44" width="48" height="36" rx="13" fill="#15151f" ${O}/>`,
      `<g fill="${A}" filter="url(#glow)"><g${blink}><ellipse cx="${90 + px}" cy="${60 + py}" rx="4" ry="5"/><ellipse cx="${110 + px}" cy="${60 + py}" rx="4" ry="5"/></g>`);
    if (state === 'talking') s.push('<rect x="93" y="69" width="14" height="6" rx="3" class="eq"/>');
    else s.push(`<path d="${state === 'thinking' ? 'M95 73L105 70' : 'M94 72L106 72'}" stroke="${A}" stroke-width="2.5" stroke-linecap="round"/>`);
    s.push('</g>');
  } else {
    s.push(`<circle cx="72" cy="66" r="6" fill="${skin}" ${O}/><circle cx="128" cy="66" r="6" fill="${skin}" ${O}/>`,
      `<path d="M72 60Q72 30 100 30Q128 30 128 60Q128 88 112 96Q100 101 88 96Q72 88 72 60Z" fill="${skin}" ${O}/>`,
      '<circle cx="83" cy="77" r="4.5" fill="#e8737a" opacity=".22"/><circle cx="117" cy="77" r="4.5" fill="#e8737a" opacity=".22"/>');
    if (d.facialHair === 'stubble') s.push(`<path d="M74 70Q76 92 100 100Q124 92 126 70Q120 92 100 93Q80 92 74 70Z" fill="${hc}" opacity=".35"/>`);
    if (d.facialHair === 'beard') s.push(`<path d="M72 60Q70 100 100 118Q130 100 128 60Q124 82 112 86Q100 79 88 86Q76 82 72 60Z" fill="${hc}" ${O}/>`);

    const eye = (cx: number, dir: number) => {
      if (d.eyes === 'sharp') return `<path d="M${cx - 5 * dir} 65Q${cx} 59 ${cx + 6 * dir} 63Q${cx + dir} 68 ${cx - 5 * dir} 65Z" fill="#fff" stroke="${ink}" stroke-width="1.5"/><circle cx="${cx + px}" cy="${64 + py}" r="2.4" fill="${ink}"/>`;
      const e = `<ellipse cx="${cx}" cy="64" rx="4.5" ry="5" fill="#fff" stroke="${ink}" stroke-width="1.5"/><circle cx="${cx + px}" cy="${64.5 + py}" r="2.8" fill="${ink}"/><circle cx="${cx + px + 1}" cy="${63 + py}" r=".9" fill="#fff"/>`;
      return d.eyes === 'sleepy' ? e + `<path d="M${cx - 4.5} 64.5A4.5 5 0 0 1 ${cx + 4.5} 64.5Z" fill="${skinSh}" stroke="${ink}" stroke-width="1.5"/>` : e;
    };
    s.push(`<g${blink}>${eye(89, 1)}${eye(111, -1)}</g>`);
    const brows = state === 'thinking' ? 'M84 55L94 54M106 51L116 48'
      : d.eyes === 'sharp' ? 'M84 54L94 56M106 56L116 54' : 'M84 55L94 54M106 54L116 55';
    s.push(`<path d="${brows}" stroke="${dark(hc, 0.25)}" stroke-width="2.4" stroke-linecap="round"/>`,
      `<path d="M100 68Q97 75 100 76" stroke="${skinSh}" stroke-width="1.6" fill="none" stroke-linecap="round"/>`);

    const smile = `<path d="M94 83Q100 87 106 83" stroke="${ink}" stroke-width="1.8" fill="none" stroke-linecap="round"/>`;
    if (state === 'talking') s.push(`<g class="mo"><path d="M94 82Q100 93 106 82Z" fill="#5a1f24" ${O}/></g><g class="mc">${smile}</g>`);
    else if (state === 'thinking') s.push(`<path d="M96 85Q100 84 104 86" stroke="${ink}" stroke-width="1.8" fill="none" stroke-linecap="round"/>`);
    else s.push(smile);
    if (d.facialHair === 'mustache' || d.facialHair === 'beard') s.push(`<path d="M89 81Q95 76 100 79Q105 76 111 81Q105 83 100 81Q95 83 89 81Z" fill="${hc}" stroke="${ink}" stroke-width="1"/>`);

    // Front hair.
    const fringe = 'M70 66Q68 26 100 25Q132 26 130 66Q128 46 116 42Q108 54 90 50Q80 48 74 56Q72 60 70 66Z';
    const front = {
      short: 'M70 64Q68 26 100 25Q132 26 130 64Q126 50 118 44Q104 50 84 44Q74 50 70 64Z',
      bob: fringe, ponytail: fringe, twintails: fringe,
      bun: 'M70 64Q68 28 100 27Q132 28 130 64Q126 48 100 44Q74 48 70 64Z',
      long: 'M70 70Q66 24 100 24Q134 26 130 70Q126 48 112 40Q96 52 76 50Q72 58 70 70Z',
      spiky: 'M70 64L66 40L78 44L76 24L90 34L96 16L106 32L118 20L120 38L134 34L130 64Q124 48 100 46Q78 48 70 64Z',
    }[d.hair as string];
    if (front) s.push(`<path d="${front}" fill="${hc}" ${O}/>`);
    if (d.hair === 'long') s.push(`<path d="M72 64Q64 110 70 146Q78 152 84 144Q78 110 82 76Z M128 64Q136 110 130 146Q122 152 116 144Q122 110 118 76Z" fill="${hc}" ${O}/>`);
    if (d.hair === 'curly') {
      const c: string[] = [];
      for (let i = 0; i <= 8; i++) {
        const a = Math.PI * (1.06 - i * 1.12 / 8);
        c.push(`<circle cx="${(100 + 30 * Math.cos(a)).toFixed(1)}" cy="${(54 - 26 * Math.sin(a)).toFixed(1)}" r="10"/>`);
      }
      // Outline pass under a fill pass, so the overlapping curls share one outline.
      s.push(`<g fill="${ink}" stroke="${ink}" stroke-width="4">${c.join('')}</g><g fill="${hc}">${c.join('')}</g>`);
    }
    if (d.accessory === 'glasses') s.push(`<g fill="#ffffff22" stroke="${ink}" stroke-width="2"><circle cx="89" cy="64" r="7.5"/><circle cx="111" cy="64" r="7.5"/><path d="M96.5 63Q100 61 103.5 63M81.5 63L73 61M118.5 63L127 61" fill="none"/></g>`);
  }

  if (d.accessory === 'headphones') s.push(
    `<path d="M68 66Q66 16 100 16Q134 16 132 66" fill="none" stroke="${ink}" stroke-width="8"/><path d="M68 66Q66 16 100 16Q134 16 132 66" fill="none" stroke="#3a3b4a" stroke-width="4"/>`,
    `<rect x="61" y="54" width="13" height="24" rx="5" fill="#3a3b4a" ${O}/><rect x="126" y="54" width="13" height="24" rx="5" fill="#3a3b4a" ${O}/>`,
    `<rect x="64" y="59" width="4" height="14" rx="2" fill="${A}"/><rect x="132" y="59" width="4" height="14" rx="2" fill="${A}"/>`);
  if (d.accessory === 'hat') s.push(
    `<path d="M76 42Q76 12 100 12Q124 12 124 42Z" fill="#2c2e3b" ${O}/><rect x="76" y="32" width="48" height="8" fill="${A}" ${O}/>`,
    `<ellipse cx="100" cy="42" rx="40" ry="7" fill="#2c2e3b" ${O}/>`);

  // Arms last, so a hand can rest on the chin.
  const arm: [string, number, number] = {
    idle: [`M${R} 122Q${R + 12} 160 ${R + 6} 198`, R + 6, 203] as [string, number, number],
    thinking: [`M${R} 122L${R + 12} 168L117 112`, 114, 105] as [string, number, number],
    talking: [`M${R} 122L${R + 16} 166L${R + 28} 142`, R + 29, 137] as [string, number, number],
  }[state];
  for (const [p, hx, hy] of [[`M${L} 122Q${L - 12} 160 ${L - 6} 198`, L - 6, 203] as [string, number, number], arm]) {
    s.push(`<path d="${p}" fill="none" stroke="${ink}" stroke-width="20" stroke-linecap="round" stroke-linejoin="round"/>`,
      `<path d="${p}" fill="none" stroke="${fill}" stroke-width="16" stroke-linecap="round" stroke-linejoin="round"/>`,
      `<circle cx="${hx}" cy="${hy}" r="8" fill="${skin}" ${O}/>`);
  }
  if (d.outfit === 'armor') s.push(`<g fill="#e8e8f2" ${O}><circle cx="${L}" cy="120" r="11"/><circle cx="${R}" cy="120" r="11"/></g><path d="M${L - 6} 116L${L + 6} 116M${R - 6} 116L${R + 6} 116" stroke="${A}" stroke-width="2.5"/>`);

  if (state === 'thinking') s.push(
    `<circle cx="140" cy="42" r="3" fill="#fff" ${O}/><circle cx="149" cy="31" r="4.5" fill="#fff" ${O}/>`,
    `<ellipse cx="168" cy="16" rx="20" ry="12" fill="#fff" ${O}/>`,
    `<g fill="${ink}"><circle class="dot" cx="160" cy="16" r="2.2"/><circle class="dot" style="animation-delay:.2s" cx="168" cy="16" r="2.2"/><circle class="dot" style="animation-delay:.4s" cx="176" cy="16" r="2.2"/></g>`);

  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 300" width="400" height="600">
<style>
.blink{animation:blink 4.5s infinite;transform-box:fill-box;transform-origin:center}
@keyframes blink{0%,93%,100%{transform:scaleY(1)}95%{transform:scaleY(.1)}}
.mo,.mc{animation:mo .32s steps(1) infinite}.mc{animation-delay:-.16s}
@keyframes mo{0%{opacity:1}50%{opacity:0}}
.eq{animation:eq .28s ease-in-out infinite alternate;transform-box:fill-box;transform-origin:center}
@keyframes eq{to{transform:scaleY(.3)}}
.dot{animation:dot 1.2s infinite}@keyframes dot{0%,100%{opacity:.2}50%{opacity:1}}
.fig{animation:br 3.6s ease-in-out infinite}@keyframes br{50%{transform:translateY(1.2px)}}
@media (prefers-reduced-motion:reduce){*{animation:none!important}}
</style>
<defs>
<radialGradient id="halo"><stop offset="0" stop-color="${A}" stop-opacity=".32"/><stop offset="1" stop-color="${A}" stop-opacity="0"/></radialGradient>
<filter id="glow" x="-50%" y="-50%" width="200%" height="200%"><feGaussianBlur stdDeviation="1.6" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter>
</defs>
<ellipse cx="100" cy="150" rx="96" ry="140" fill="url(#halo)"/>
<g class="fig">${s.join('')}</g>
</svg>`;
}

// As an <img> src: the SVG's own animations still run, and its markup stays out of the page.
export const dollURL = (raw: unknown, state: PortraitState, accent: string) =>
  'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(renderDoll(raw, state, accent));
