<script setup lang="ts">
import { nextTick, ref } from 'vue';

// The current picks of a multi-select popover, pinned below its scrolling list so they stay in view whatever the search or scroll
// position. Each chip removes its pick. Renders nothing while nothing is picked; the popover is open whenever this exists, and a pick
// list is short (it is what the user clicked), so this costs nothing at cluster scale.

export interface Pick {
    key: string;
    label: string;
}

defineProps<{ items: readonly Pick[]; label: string }>();
const emit = defineEmits<{ remove: [key: string] }>();
const el = ref<HTMLElement | null>(null);

// Removing the focused chip would drop focus to <body> (and the popover's Esc handler with it): move it to the chip that takes its
// place, or to the popover's search field once the last one is gone.
async function remove(key: string, i: number) {
    const dialog = el.value?.closest<HTMLElement>('[role=dialog]');
    emit('remove', key);
    await nextTick();
    const chips = el.value?.querySelectorAll<HTMLButtonElement>('.pchip');
    const next = chips?.length ? chips[Math.min(i, chips.length - 1)] : dialog?.querySelector<HTMLInputElement>('input[type=search]');
    next?.focus();
}
</script>

<template>
    <div v-if="items.length" ref="el" class="picked" role="group" :aria-label="label" data-testid="selected-chips">
        <div class="ph">{{ label }} <span class="muted">{{ items.length }}</span></div>
        <div class="pchips">
            <button
                v-for="(it, i) in items"
                :key="it.key"
                class="pchip"
                type="button"
                :title="`Remove ${it.label}`"
                :aria-label="`Remove ${it.label}`"
                @click="remove(it.key, i)"
            >
                <span class="pl">{{ it.label }}</span>
                <svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
                    <path d="M4 4l8 8M12 4l-8 8" />
                </svg>
            </button>
        </div>
    </div>
</template>

<style scoped>
.picked {
    flex: none;
    border-top: 1px solid var(--border);
    padding-top: 8px;
}
.ph {
    padding: 0 6px 6px;
    font-size: 12px;
    font-weight: 600;
    color: var(--text2);
}
.pchips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    max-height: 84px;
    overflow: auto;
    padding: 0 2px 2px;
}
.pchip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    max-width: 100%;
    height: 24px;
    padding: 0 6px 0 8px;
    border: 1px solid var(--accent);
    border-radius: 6px;
    background: var(--accent-soft);
    color: var(--text);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
}
.pchip svg {
    flex: none;
    color: var(--text3);
}
.pchip:hover svg {
    color: var(--text);
}
.pchip:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 1px;
}
.pl {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
@media (pointer: coarse) {
    .pchip {
        height: 44px;
    }
    .pchips {
        max-height: 140px;
    }
}
</style>
