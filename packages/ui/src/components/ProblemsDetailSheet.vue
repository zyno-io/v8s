<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue';

import type { EvState, ProblemRow, SnoozePreset } from '@/lib/problems.ts';

import ProblemsEvidence from '@/components/ProblemsEvidence.vue';

defineProps<{ row: ProblemRow; state: EvState | null }>();
const emit = defineEmits<{ close: []; ack: [preset: SnoozePreset | null]; unack: []; open: [] }>();

const back = ref<HTMLButtonElement | null>(null);
let returnTo: HTMLElement | null = null;

function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
        e.preventDefault();
        emit('close');
    }
}
onMounted(() => {
    returnTo = document.activeElement as HTMLElement | null;
    back.value?.focus();
    window.addEventListener('keydown', onKey);
});
onBeforeUnmount(() => {
    window.removeEventListener('keydown', onKey);
    returnTo?.focus?.();
});
</script>

<template>
    <div class="sheet" role="dialog" aria-modal="true" :aria-label="`${row.kind} ${row.name}`" data-testid="problem-sheet">
        <header class="sh">
            <button ref="back" class="btn ghost" type="button" aria-label="Back to problems" @click="emit('close')">
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
                    <path d="M10 3L5 8l5 5" />
                </svg>
            </button>
            <div class="who">
                <b class="n">{{ row.kind }} {{ row.name }}</b>
                <span class="muted ns">{{ row.namespace || 'cluster-scoped' }}</span>
            </div>
            <button class="btn sm" type="button" @click="emit('open')">Open</button>
        </header>
        <div class="scroll">
            <ProblemsEvidence :row="row" :state="state" phone @ack="p => emit('ack', p)" @unack="emit('unack')" />
        </div>
    </div>
</template>

<style scoped>
.sheet {
    position: fixed;
    inset: 0;
    z-index: 40;
    background: var(--surface);
    display: flex;
    flex-direction: column;
}
.sh {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
}
.who {
    flex-grow: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
}
.n,
.ns {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.ns {
    font-size: 12px;
}
.scroll {
    flex: 1 1 0;
    overflow: auto;
    min-height: 0;
}
</style>
