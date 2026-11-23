<script setup lang="ts">
import type { ObjectRef, ShellHandle } from '@v8s/client';

import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import SmartSelect from '@/components/SmartSelect.vue';
import { getAgent } from '@/lib/agent.ts';
import { shellRequest } from '@/lib/drawer.ts';
import { errorText, isCancelled } from '@/lib/mutations.ts';
import {
    connectBlocker,
    connectNote,
    containerChoice,
    DEBUG_IMAGE_KEY,
    DEFAULT_DEBUG_IMAGE,
    debugParams,
    execParams,
    looksLikeNoShell
} from '@/lib/shell.ts';
import { listOptions } from '@/lib/smart-select.ts';
import { isPhone } from '@/lib/viewport.ts';
import { withWriteConfirm } from '@/lib/write-confirm.ts';

const TerminalView = defineAsyncComponent(() => import('./TerminalView.vue'));

const props = defineProps<{ target: ObjectRef; parsed: Record<string, unknown> | null }>();

type Kind = 'exec' | 'debug';

const choice = computed(() => containerChoice(props.parsed));
const blocker = computed(() => connectBlocker(props.parsed));
const note = computed(() => connectNote(props.parsed));
const container = ref(choice.value.initial);
const containerOptions = computed(() => listOptions(choice.value.names));

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
const selectRef = ref<{ focus(): void } | null>(null);
const reconnectRef = ref<HTMLButtonElement | null>(null);
const paneRef = ref<HTMLElement | null>(null);
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

// `s` (never a click on the tab): a lone container connects at once; otherwise the container picker takes focus (Enter connects).
watch(
    shellRequest,
    want => {
        if (!want) return;
        shellRequest.value = false;
        if (handle.value && !ended.value) {
            termRef.value?.focus();
            return;
        }
        if (!connecting.value && !blocker.value && choice.value.names.length === 1) void start('exec');
        else void nextTick(() => selectRef.value?.focus());
    },
    { immediate: true }
);

// When the session ends, the terminal lets go of the keyboard (Esc closes the drawer again); Enter on Reconnect starts a new one.
watch(ended, async v => {
    if (!v) return;
    await nextTick();
    const act = document.activeElement;
    if (act && act !== document.body && !paneRef.value?.querySelector('.sp-term')?.contains(act)) return;
    reconnectRef.value?.focus({ preventScroll: true });
});

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
    <div ref="paneRef" class="sp" data-testid="shell-pane">
        <div class="sp-bar">
            <label class="sp-f">
                <span class="muted">Container</span>
                <SmartSelect
                    ref="selectRef"
                    v-model="container"
                    :options="containerOptions"
                    data-testid="shell-container"
                    :disabled="connecting"
                    aria-label="Container"
                    @keydown.enter.prevent="start('exec')"
                />
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
            <span v-else-if="note" class="tag warning" role="note" data-testid="shell-note">{{ note }}</span>
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
            <button ref="reconnectRef" type="button" class="btn sm" data-testid="shell-reconnect" @click="start(lastKind)">Reconnect</button>
        </div>

        <p v-if="noShell" class="notice" role="note">
            No shell was found in this container.
            <button type="button" class="btn sm" @click="offerDebug">Start debug container</button>
        </p>

        <div v-if="handle" class="sp-term">
            <TerminalView ref="termRef" :handle="handle" :simplified="isPhone" />
        </div>
        <p v-else-if="!connecting" class="muted sp-hint">A shell opens only when you press Connect.</p>

        <details
            v-if="!handle || ended"
            class="sp-debug"
            :open="showDebugPrompt"
            @toggle="showDebugPrompt = ($event.target as HTMLDetailsElement).open"
        >
            <summary>
                <svg
                    class="sp-chev"
                    width="12"
                    height="12"
                    viewBox="0 0 16 16"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.6"
                    aria-hidden="true"
                >
                    <path d="M6 4l4 4-4 4" />
                </svg>
                <span class="sp-debug-title">Debug container</span>
                <span class="muted">for distroless or shell-less images</span>
            </summary>
            <div class="sp-debug-body">
                <p class="muted">
                    Adds an ephemeral container with this image to the pod, sharing the process namespace of
                    <span class="mono">{{ container || 'the selected container' }}</span>.
                </p>
                <div class="sp-row">
                    <input
                        v-model="image"
                        class="field mono sp-image"
                        type="text"
                        spellcheck="false"
                        placeholder="Image"
                        aria-label="Debug container image"
                        data-testid="shell-debug-image"
                        @keydown.enter.prevent="container && start('debug')"
                    />
                    <button type="button" class="btn" data-testid="shell-debug" :disabled="connecting || !container" @click="start('debug')">
                        Start debug container
                    </button>
                </div>
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
.sp-debug {
    flex: none;
    margin-top: 8px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
}
.sp-debug summary {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 36px;
    padding: 0 12px;
    cursor: pointer;
    list-style: none;
    border-radius: 8px;
}
.sp-debug summary::-webkit-details-marker {
    display: none;
}
.sp-debug summary:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.sp-chev {
    flex: none;
    color: var(--text3);
    transition: transform 0.12s ease;
}
.sp-debug[open] .sp-chev {
    transform: rotate(90deg);
}
.sp-debug-title {
    font-weight: 600;
}
.sp-debug summary .muted {
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
}
.sp-debug-body {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px 12px 12px;
    border-top: 1px solid var(--border);
}
.sp-debug-body p {
    margin: 0;
}
.sp-debug .sp-row {
    align-items: center;
}
.sp-image {
    flex: 1 1 220px;
    min-width: 0;
    max-width: 360px;
}
</style>
