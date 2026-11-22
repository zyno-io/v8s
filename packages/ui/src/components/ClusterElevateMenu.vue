<script setup lang="ts">
import { computed, nextTick, ref } from 'vue';

import { elevateItem } from '@/lib/elevate.ts';
import { useClustersStore } from '@/stores/clusters.ts';

// The small arrow beside a cluster's status: one menu item, Elevate to its linked write context or Revert. The menu is teleported to
// <body> with fixed positioning (like CheckMenu) so a toolbar's overflow does not clip it.

const props = defineProps<{ id: string }>();
const clusters = useClustersStore();

const name = computed(() => clusters.nameById.get(props.id) ?? props.id);
const item = computed(() => elevateItem(name.value, clusters.writeContextOf(props.id), clusters.isElevated(props.id)));
const busy = computed(() => clusters.elevating.has(props.id));

const open = ref(false);
const btnEl = ref<HTMLButtonElement | null>(null);
const menuEl = ref<HTMLDivElement | null>(null);
const pos = ref({ top: 0, left: 0 });
const WIDTH = 264;

async function toggleMenu() {
    if (open.value) return close();
    const r = btnEl.value?.getBoundingClientRect();
    if (r) pos.value = { top: r.bottom + 4, left: Math.max(8, Math.min(r.left, window.innerWidth - WIDTH - 8)) };
    open.value = true;
    await nextTick();
    menuEl.value?.querySelector<HTMLButtonElement>('button')?.focus();
}
function close() {
    open.value = false;
    btnEl.value?.focus();
}
function run() {
    const it = item.value;
    if (!it || busy.value) return;
    open.value = false;
    btnEl.value?.focus();
    void clusters.setElevated(props.id, it.elevate);
}
</script>

<template>
    <span v-if="item" class="cem" @keydown.esc.stop="open && close()">
        <button
            ref="btnEl"
            class="cem-btn"
            type="button"
            aria-haspopup="menu"
            :aria-expanded="open"
            :aria-label="`Context of ${name}`"
            :data-testid="`elevate-menu-${id}`"
            @click.stop="toggleMenu"
        >
            <svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
                <path d="M4 6l4 4 4-4" />
            </svg>
        </button>
        <Teleport v-if="open" to="body">
            <div class="cem-back" @mousedown="close"></div>
            <div
                ref="menuEl"
                class="floating cem-menu"
                role="menu"
                :aria-label="`Context of ${name}`"
                :style="{ top: pos.top + 'px', left: pos.left + 'px', width: WIDTH + 'px' }"
                @keydown.esc.stop="close"
            >
                <button class="cem-item" type="button" role="menuitem" :disabled="busy" data-testid="elevate-item" @click="run">
                    <span class="cem-l">{{ busy ? 'Switching…' : item.label }}</span>
                    <span v-if="!busy" class="cem-hint muted">{{ item.hint }}</span>
                </button>
            </div>
        </Teleport>
    </span>
</template>

<style scoped>
.cem {
    display: inline-flex;
    margin-left: 2px;
}
.cem-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 16px;
    height: 16px;
    padding: 0;
    border: 0;
    border-radius: 4px;
    background: transparent;
    color: inherit;
    opacity: 0.7;
    cursor: pointer;
}
.cem-btn:hover,
.cem-btn[aria-expanded='true'] {
    opacity: 1;
    background: var(--hover);
}
.cem-btn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 1px;
}
@media (pointer: coarse) {
    .cem-btn {
        width: 44px;
        height: 44px;
        margin: -14px 0;
    }
}
.cem-back {
    position: fixed;
    inset: 0;
    z-index: 40;
}
.cem-menu {
    position: fixed;
    z-index: 41;
    padding: 4px;
    font-size: 13px;
    font-weight: 400;
    color: var(--text);
    white-space: normal;
}
.cem-item {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    width: 100%;
    padding: 6px 8px;
    border: 0;
    border-radius: 5px;
    background: transparent;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
}
.cem-item:hover:not(:disabled) {
    background: var(--hover);
}
.cem-item:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.cem-item:disabled {
    cursor: default;
    color: var(--text3);
}
.cem-hint {
    font-size: 11px;
}
@media (pointer: coarse) {
    .cem-item {
        min-height: 44px;
    }
}
</style>
