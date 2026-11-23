<script setup lang="ts">
import { computed, nextTick, ref } from 'vue';

import { elevateItem } from '@/lib/elevate.ts';
import { useClustersStore } from '@/stores/clusters.ts';

// A cluster's context menu: one item, Elevate to its linked write context or Revert. The trigger is the whole default slot (a status
// chip) plus a small arrow; without a link the slot renders as a plain element. Attributes (class, title, data-testid) land on the
// trigger. The menu is teleported to <body> with fixed positioning (like CheckMenu) so a toolbar's overflow does not clip it.

defineOptions({ inheritAttrs: false });
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
    <span v-if="!item" v-bind="$attrs"><slot /></span>
    <span v-else class="cem" @keydown.esc.stop="open && close()">
        <button
            v-bind="$attrs"
            ref="btnEl"
            class="cem-btn"
            type="button"
            aria-haspopup="menu"
            :aria-expanded="open"
            :data-elevate-menu="id"
            @click.stop="toggleMenu"
        >
            <slot /><svg
                class="cem-arrow"
                width="10"
                height="10"
                viewBox="0 0 16 16"
                fill="none"
                stroke="currentColor"
                stroke-width="1.8"
                aria-hidden="true"
            >
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
                    <span class="cem-l">{{ busy ? (clusters.elevating.get(id) ? 'Elevating…' : 'Reverting…') : item.label }}</span>
                    <span v-if="!busy" class="cem-hint muted">{{ item.hint }}</span>
                </button>
            </div>
        </Teleport>
    </span>
</template>

<style scoped>
.cem {
    display: inline-flex;
}
/* The trigger keeps the look of whatever it wraps (a .tag chip): only the button chrome is reset. */
.cem-btn {
    font-family: inherit;
    cursor: pointer;
}
.cem-btn:hover,
.cem-btn[aria-expanded='true'] {
    box-shadow: 0 0 0 1px var(--bstrong);
}
.cem-btn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 1px;
}
.cem-arrow {
    margin-left: 4px;
    opacity: 0.75;
    flex: none;
}
@media (pointer: coarse) {
    .cem-btn {
        min-height: 44px;
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
