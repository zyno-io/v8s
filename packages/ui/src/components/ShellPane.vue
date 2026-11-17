<script setup lang="ts">
import type { ObjectRef, ShellHandle } from '@v8s/client';

import { computed, defineAsyncComponent, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import { getAgent } from '@/lib/agent.ts';
import { errorText, isCancelled } from '@/lib/mutations.ts';
import { connectBlocker, containerChoice, DEBUG_IMAGE_KEY, DEFAULT_DEBUG_IMAGE, debugParams, execParams, looksLikeNoShell } from '@/lib/shell.ts';
import { isPhone } from '@/lib/viewport.ts';
import { withWriteConfirm } from '@/lib/write-confirm.ts';

const TerminalView = defineAsyncComponent(() => import('./TerminalView.vue'));

const props = defineProps<{ target: ObjectRef; parsed: Record<string, unknown> | null }>();

type Kind = 'exec' | 'debug';

const choice = computed(() => containerChoice(props.parsed));
const blocker = computed(() => connectBlocker(props.parsed));
const container = ref(choice.value.initial);

function loadImage(): string {
    try {
        return localStorage.getItem(DEBUG_IMAGE_KEY) || DEFAULT_DEBUG_IMAGE;
    } catch {
        return DEFAULT_DEBUG_IMAGE;
    }
}
const image = ref(loadImage());

const handle = shallowRef<ShellHandle | null>(null);
const opening = ref(false);
const openError = ref('');
const lastKind = ref<Kind>('exec');
const startedAt = ref(0);
const termRef = ref<{ focus(): void } | null>(null);
let seq = 0;

const hState = computed(() => handle.value?.state.value ?? null);
const ready = computed(() => handle.value?.ready.value ?? null);
const exit = computed(() => handle.value?.exit.value ?? null);
const connecting = computed(() => opening.value || hState.value === 'connecting');
const connected = computed(() => hState.value === 'ready');
const ended = computed(() => !!exit.value);
const writeCtx = computed(() => {
    const c = handle.value?.usedContext;
    return c || '';
});
const noShell = computed(
    () => lastKind.value === 'exec' && ended.value && looksLikeNoShell(exit.value) && (!ready.value || Date.now() - startedAt.value < 15000)
);
const statusText = computed(() => {
    if (connecting.value) return lastKind.value === 'debug' ? 'Starting debug container…' : 'Connecting…';
    if (connected.value) return 'Connected';
    if (ended.value) return 'Session ended';
    return 'Not connected';
});

function disconnect() {
    seq++;
    const h = handle.value;
    handle.value = null;
    opening.value = false;
    if (h) {
        h.onData = null;
        h.close();
    }
}

async function start(kind: Kind) {
    if (opening.value) return;
    if (kind === 'exec' && blocker.value) return;
    const t = props.target;
    disconnect();
    const mine = seq;
    lastKind.value = kind;
    openError.value = '';
    opening.value = true;
    startedAt.value = Date.now();
    if (kind === 'debug') {
        try {
            localStorage.setItem(DEBUG_IMAGE_KEY, image.value.trim() || DEFAULT_DEBUG_IMAGE);
        } catch {
            /* storage unavailable */
        }
    }
    try {
        const agent = getAgent();
        const h = await withWriteConfirm(t.cluster, extra =>
            kind === 'exec'
                ? agent.openStream('exec', { ...execParams(t, container.value), ...extra })
                : agent.openStream('debug', { ...debugParams(t, container.value, image.value), ...extra })
        );
        if (mine !== seq) {
            h.onData = null;
            h.close();
            return;
        }
        handle.value = h;
    } catch (e) {
        if (mine === seq && !isCancelled(e)) openError.value = errorText(e);
    } finally {
        if (mine === seq) opening.value = false;
    }
}

const targetKey = computed(() => `${props.target.cluster}|${props.target.gvr}|${props.target.namespace ?? ''}|${props.target.name ?? ''}`);
watch(targetKey, () => {
    disconnect();
    openError.value = '';
    container.value = choice.value.initial;
});
watch(
    () => choice.value.names.join('\0'),
    () => {
        if (!choice.value.names.includes(container.value)) container.value = choice.value.initial;
    }
);
onBeforeUnmount(disconnect);

const showDebugPrompt = ref(false);
watch(noShell, v => {
    if (v) showDebugPrompt.value = true;
});
function offerDebug() {
    showDebugPrompt.value = true;
    void start('debug');
}
</script>

<template>
    <div class="sp" data-testid="shell-pane">
        <div class="sp-bar">
            <label class="sp-f">
                <span class="muted">Container</span>
                <select
                    v-model="container"
                    class="field"
                    data-testid="shell-container"
                    :disabled="connecting"
                    aria-label="Container"
                    @keydown.enter.prevent="start('exec')"
                >
                    <option v-for="c in choice.names" :key="c" :value="c">{{ c }}</option>
                </select>
            </label>
            <button
                type="button"
                class="btn primary"
                data-testid="shell-connect"
                :disabled="!!blocker || connecting"
                :title="blocker || undefined"
                @click="start('exec')"
            >
                Connect
            </button>
            <button v-if="handle && !ended" type="button" class="btn" @click="disconnect">Disconnect</button>
            <span v-if="blocker" class="muted" role="note">{{ blocker }}</span>
            <span class="grow"></span>
            <span v-if="connected && ready?.shell" class="tag info">{{ ready.shell }}</span>
            <span v-if="connected && ready?.container" class="tag">{{ ready.container }}</span>
            <span v-if="writeCtx" class="tag warning">write: {{ writeCtx }}</span>
        </div>

        <p class="sp-status muted" role="status" aria-live="polite" data-testid="shell-status">{{ statusText }}</p>

        <p v-if="openError" class="notice danger" role="alert">{{ openError }}</p>

        <div v-if="ended" class="notice" role="status" data-testid="shell-ended">
            <strong>Session ended</strong>
            <span> exit code {{ exit?.code }}<template v-if="exit?.error">: {{ exit.error }}</template> </span>
            <button type="button" class="btn sm" data-testid="shell-reconnect" @click="start(lastKind)">Reconnect</button>
        </div>

        <p v-if="noShell" class="notice" role="note">
            No shell was found in this container.
            <button type="button" class="btn sm" @click="offerDebug">Start debug container</button>
        </p>

        <div v-if="handle" class="sp-term">
            <TerminalView ref="termRef" :handle="handle" :simplified="isPhone" />
        </div>
        <p v-else-if="!connecting" class="muted sp-hint">A shell opens only when you press Connect.</p>

        <details class="sp-debug" :open="showDebugPrompt">
            <summary>Debug container</summary>
            <p class="muted">
                For distroless or shell-less containers: starts an ephemeral container with this image sharing the selected container's process
                namespace.
            </p>
            <div class="sp-row">
                <label class="sp-f">
                    <span class="muted">Image</span>
                    <input
                        v-model="image"
                        class="field mono"
                        type="text"
                        spellcheck="false"
                        aria-label="Debug container image"
                        data-testid="shell-debug-image"
                    />
                </label>
                <span class="muted">Target: <span class="mono">{{ container }}</span></span>
                <button type="button" class="btn" data-testid="shell-debug" :disabled="connecting || !container" @click="start('debug')">
                    Start debug container
                </button>
            </div>
        </details>
    </div>
</template>

<style scoped>
.sp {
    display: flex;
    flex-direction: column;
    gap: 8px;
    height: 100%;
    min-height: 0;
}
.sp-bar,
.sp-row {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 8px;
}
.sp-f {
    display: flex;
    flex-direction: column;
    gap: 2px;
}
.grow {
    flex: 1;
}
.sp-status {
    margin: 0;
}
.sp-term {
    flex: 1;
    min-height: 320px;
    border-radius: 6px;
    overflow: hidden;
}
.sp-hint {
    margin: 0;
}
.notice {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
}
.sp-debug summary {
    cursor: pointer;
    min-height: 32px;
}
</style>
