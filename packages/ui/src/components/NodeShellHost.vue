<script setup lang="ts">
import type { ShellHandle } from '@v8s/client';

import { computed, defineAsyncComponent, onBeforeUnmount, ref, watch } from 'vue';

import { getAgent } from '@/lib/agent.ts';
import { errorText, isCancelled } from '@/lib/mutations.ts';
import { nodeShellNamespace, nodeShellRequest } from '@/lib/node-shell.ts';
import { createSessionSlot } from '@/lib/session-slot.ts';
import { nodeShellParams, privilegedDenied } from '@/lib/shell.ts';
import { withWriteConfirm } from '@/lib/write-confirm.ts';
import { useClustersStore } from '@/stores/clusters.ts';

const clusters = useClustersStore();
const TerminalView = defineAsyncComponent(() => import('./TerminalView.vue'));

const req = nodeShellRequest;
const slot = createSessionSlot<ShellHandle>();
const { handle, opening, release } = slot;
const error = ref('');
const height = ref(360);

const ready = computed(() => handle.value?.ready.value ?? null);
const exit = computed(() => handle.value?.exit.value ?? null);
const connecting = computed(() => opening.value || handle.value?.state.value === 'connecting');
const problem = computed(() => error.value || exit.value?.error || '');
const nsName = computed(() => (req.value ? nodeShellNamespace(req.value.cluster) : ''));
const denied = computed(() => privilegedDenied(problem.value));

async function open() {
    const r = req.value;
    if (!r) return;
    error.value = '';
    try {
        const agent = getAgent();
        await slot.open(() =>
            withWriteConfirm(r.cluster, extra => agent.openStream('nodeshell', { ...nodeShellParams(r.cluster, r.node), ...extra }))
        );
    } catch (e) {
        if (!isCancelled(e)) error.value = errorText(e);
    }
}

function close() {
    release();
    error.value = '';
    req.value = null;
}

watch(
    req,
    r => {
        if (r) void open();
        else release();
    },
    { immediate: true }
);
onBeforeUnmount(release);

// Drag the top edge to resize.
function dragStart(e: PointerEvent) {
    e.preventDefault();
    const startY = e.clientY;
    const startH = height.value;
    const move = (ev: PointerEvent) => {
        height.value = Math.round(Math.min(window.innerHeight * 0.85, Math.max(160, startH + (startY - ev.clientY))));
    };
    const up = () => {
        window.removeEventListener('pointermove', move);
        window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
}
function dragKey(e: KeyboardEvent) {
    if (e.key === 'ArrowUp') height.value = Math.min(Math.round(window.innerHeight * 0.85), height.value + 24);
    else if (e.key === 'ArrowDown') height.value = Math.max(160, height.value - 24);
    else return;
    e.preventDefault();
}
</script>

<template>
    <section
        v-if="req"
        class="ns-dock"
        data-testid="node-shell-dock"
        role="region"
        :aria-label="`Node shell on ${req.node}`"
        :style="{ height: height + 'px' }"
    >
        <div
            class="ns-grip"
            role="separator"
            aria-orientation="horizontal"
            aria-label="Resize node shell"
            tabindex="0"
            @pointerdown="dragStart"
            @keydown="dragKey"
        ></div>
        <header class="ns-head">
            <span class="tag">{{ req.node }}</span>
            <span class="tag info">{{ clusters.displayName(req.cluster) }}</span>
            <span class="tag danger">privileged pod on {{ req.node }}</span>
            <span v-if="ready?.pod" class="muted mono">{{ ready.pod }}</span>
            <span v-if="ready?.attached !== undefined" class="muted">{{ ready.attached ? 'attached to first session' : 'new nsenter session' }}</span>
            <span v-if="handle?.usedContext" class="tag warning">write: {{ handle.usedContext }}</span>
            <span class="muted" role="status" aria-live="polite">{{ connecting ? 'Starting privileged pod…' : '' }}</span>
            <span class="grow"></span>
            <button type="button" class="btn sm" @click="close">Close</button>
        </header>
        <p v-if="error" class="notice danger" role="alert">
            {{ error }}<template v-if="denied"> — this cluster does not allow privileged pods in {{ nsName }}.</template>
        </p>
        <div v-if="exit" class="notice" role="status">
            <strong>Session ended</strong>
            <span>
                exit code {{ exit.code }}<template v-if="exit.error">: {{ exit.error }}</template
                ><template v-if="denied && !error"> — this cluster does not allow privileged pods in {{ nsName }}.</template>
            </span>
            <button type="button" class="btn sm" @click="open">Reconnect</button>
        </div>
        <p v-if="error" class="ns-retry"><button type="button" class="btn sm" @click="open">Retry</button></p>
        <div v-if="handle" class="ns-term">
            <TerminalView :handle="handle" />
        </div>
    </section>
</template>

<style scoped>
.ns-dock {
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 40;
    display: flex;
    flex-direction: column;
    background: var(--surface, #14151a);
    border-top: 1px solid var(--border, #2a2d36);
    box-shadow: 0 -4px 16px rgb(0 0 0 / 25%);
}
.ns-grip {
    height: 6px;
    cursor: ns-resize;
    background: var(--border, #2a2d36);
    touch-action: none;
}
.ns-grip:focus-visible {
    outline: 2px solid var(--accent, #6aa7f5);
}
.ns-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
}
.grow {
    flex: 1;
}
.ns-term {
    flex: 1;
    min-height: 0;
}
.ns-retry {
    margin: 0 10px 6px;
}
.notice {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin: 0 10px 6px;
}
</style>
