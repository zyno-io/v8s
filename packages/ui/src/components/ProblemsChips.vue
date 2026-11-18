<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue';

import type { FamilyChip } from '@/lib/problems.ts';

import { formatCount } from '@/lib/format.ts';
import { severityTone } from '@/lib/problems.ts';
import { useClustersStore } from '@/stores/clusters.ts';

const props = defineProps<{ chips: FamilyChip[]; selected: readonly string[] }>();
const emit = defineEmits<{ toggle: [family: string]; setClass: [cls: string, on: boolean] }>();

const clusters = useClustersStore();
const openFamily = ref('');
// The chip row scrolls sideways, which would clip an absolutely placed popover: it is fixed instead, placed
// under its chip when it opens. The backdrop blocks scrolling under it; resize or any scroll closes it.
const popAt = ref({ left: 0, top: 0 });
const chipEls = new Map<string, HTMLElement>();
const POP_W = 440;
function open(family: string) {
    const el = chipEls.get(family);
    if (el) {
        const r = el.getBoundingClientRect();
        const w = Math.min(POP_W, window.innerWidth - 24);
        popAt.value = { left: Math.max(12, Math.min(r.left, window.innerWidth - w - 12)), top: r.bottom + 6 };
    }
    openFamily.value = family;
    addEventListener('resize', close);
    addEventListener('scroll', onScroll, true);
}
// a scroll inside the popover (a long class list) keeps it open
const onScroll = (e: Event) => (e.target instanceof Element && e.target.closest('.pop') ? undefined : close());
function close() {
    openFamily.value = '';
    removeEventListener('resize', close);
    removeEventListener('scroll', onScroll, true);
}
const toggleOpen = (family: string) => (openFamily.value === family ? close() : open(family));
const setChipEl = (family: string, el: unknown) => {
    if (el instanceof HTMLElement) chipEls.set(family, el);
    else chipEls.delete(family);
};
onBeforeUnmount(close);
let pressTimer: ReturnType<typeof setTimeout> | undefined;
let longPressed = false;

function clusterNames(ids: string[]): string {
    return ids.map(i => clusters.nameById.get(i) ?? i).join(', ');
}
function onChip(c: FamilyChip) {
    if (longPressed) {
        longPressed = false;
        return;
    }
    // An off chip has nothing to filter: it opens its class switches instead.
    if (c.off) toggleOpen(c.family);
    else emit('toggle', c.family);
}
function down(c: FamilyChip) {
    longPressed = false;
    clearTimeout(pressTimer);
    pressTimer = setTimeout(() => {
        longPressed = true;
        open(c.family);
    }, 500);
}
const up = () => clearTimeout(pressTimer);
const isOn = (f: string) => props.selected.includes(f);
</script>

<template>
    <div v-for="c in chips" :key="c.family" :ref="el => setChipEl(c.family, el)" class="cw" @keydown.esc.stop="openFamily === c.family && close()">
        <div class="pair" :class="{ on: isOn(c.family) && !c.off, off: c.off }">
            <button
                class="main"
                type="button"
                :aria-pressed="c.off ? undefined : isOn(c.family)"
                :aria-label="c.off ? `${c.label}: all checks off. Show switches` : `${c.label}: ${c.count} problems. Filter`"
                :title="c.off ? 'All checks of this family are switched off' : `Show only ${c.label.toLowerCase()} problems`"
                :data-testid="`chip-${c.family}`"
                @click="onChip(c)"
                @pointerdown="down(c)"
                @pointerup="up"
                @pointercancel="up"
                @pointermove="up"
            >
                {{ c.label }} <b>{{ c.off ? 'off' : formatCount(c.count) }}</b>
            </button>
            <button
                class="caret"
                type="button"
                :aria-expanded="openFamily === c.family"
                aria-haspopup="dialog"
                :aria-label="`${c.label} checks`"
                @click="toggleOpen(c.family)"
            >
                <svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
                    <path d="M4 6l4 4 4-4" />
                </svg>
            </button>
        </div>
        <template v-if="openFamily === c.family">
            <div class="back" @mousedown="close()"></div>
            <div class="floating pop" role="dialog" :style="{ left: popAt.left + 'px', top: popAt.top + 'px' }" :aria-label="`${c.label} checks`">
                <div v-for="k in c.classes" :key="k.class" class="cls">
                    <label class="sw">
                        <input
                            class="sr-only"
                            type="checkbox"
                            role="switch"
                            :checked="k.enabled"
                            :aria-label="`${k.title} (${k.class})`"
                            :data-testid="`sw-${k.class}`"
                            @change="emit('setClass', k.class, ($event.target as HTMLInputElement).checked)"
                        />
                        <span class="track" aria-hidden="true"><span class="knob"></span></span>
                    </label>
                    <span class="ctitle">
                        <span>{{ k.title }}</span>
                        <span class="mono muted">{{ k.class }}</span>
                        <span v-if="k.overridden.length" class="muted hint"
                            >overridden in {{ k.overridden.length }} cluster{{ k.overridden.length === 1 ? '' : 's'
                            }}<template v-if="k.overridden.length <= 3"> ({{ clusterNames(k.overridden) }})</template></span
                        >
                    </span>
                    <span class="tag" :class="severityTone(k.severity)">{{ k.severity }}</span>
                    <b class="n">{{ formatCount(k.count) }}</b>
                </div>
            </div>
        </template>
    </div>
</template>

<style scoped>
.cw {
    position: relative;
    display: inline-flex;
    flex: none;
}
.pair {
    display: inline-flex;
    align-items: stretch;
    height: 28px;
    border: 1px solid var(--bstrong);
    border-radius: 8px;
    background: var(--surface);
    font-size: 12px;
    font-weight: 500;
    color: var(--text2);
    overflow: hidden;
}
.pair.on {
    border-color: var(--accent);
    background: var(--accent-soft);
    color: var(--text);
}
.pair.off {
    opacity: 0.6;
    border-style: dashed;
}
.pair button {
    all: unset;
    box-sizing: border-box;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 0 10px;
    white-space: nowrap;
}
.pair button.caret {
    padding: 0 6px 0 2px;
    color: var(--text3);
}
.pair button:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
b {
    font-weight: 600;
    color: var(--text);
}
.back {
    position: fixed;
    inset: 0;
    z-index: 20;
}
.pop {
    position: fixed;
    z-index: 21;
    width: 440px;
    max-width: calc(100vw - 24px);
    max-height: 360px;
    overflow: auto;
    padding: 6px;
}
.cls {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 36px;
    padding: 4px 6px;
    border-radius: 6px;
}
.cls:hover {
    background: var(--hover);
}
.ctitle {
    flex-grow: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    font-size: 13px;
}
.hint {
    font-size: 11px;
}
.n {
    min-width: 28px;
    text-align: right;
    font-size: 12px;
}
.sw {
    position: relative;
    display: inline-flex;
    flex: none;
    cursor: pointer;
}
.track {
    width: 30px;
    height: 18px;
    border-radius: 9px;
    background: var(--s3);
    border: 1px solid var(--bstrong);
    position: relative;
    transition: background 0.12s;
}
.knob {
    position: absolute;
    top: 1px;
    left: 1px;
    width: 14px;
    height: 14px;
    border-radius: 7px;
    background: var(--surface);
    box-shadow: var(--rest);
    transition: transform 0.12s;
}
input:checked + .track {
    background: var(--accent);
    border-color: var(--accent);
}
input:checked + .track .knob {
    transform: translateX(12px);
}
input:focus-visible + .track {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
}
@media (max-width: 639.98px) {
    .pop {
        left: 12px !important;
        right: 12px;
        top: auto !important;
        bottom: 12px;
        width: auto;
        max-height: 60vh;
    }
}
@media (pointer: coarse) {
    .pair {
        height: 36px;
    }
    .pair button.caret {
        min-width: 32px;
        justify-content: center;
    }
}
</style>
