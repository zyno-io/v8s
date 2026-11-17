<script setup lang="ts">
import type { LogStreamHandle, ObjectRef } from '@v8s/client';

import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import { getAgent } from '@/lib/agent.ts';
import {
    containersOf,
    DEFAULT_TAIL,
    defaultContainer,
    downloadFilename,
    downloadParams,
    hasPrevious,
    logTargetKind,
    streamParams,
    TAIL_OPTIONS
} from '@/lib/logs.ts';
import { errorText } from '@/lib/mutations.ts';
import { isPhone } from '@/lib/viewport.ts';
import { withWriteConfirm } from '@/lib/write-confirm.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useUiStore } from '@/stores/ui.ts';

import LogViewer from './LogViewer.vue';

const props = defineProps<{ target: ObjectRef; parsed: Record<string, unknown> | null }>();

const clusters = useClustersStore();
const ui = useUiStore();

const isPod = computed(() => logTargetKind(props.target.gvr) === 'pod');
const containers = computed(() => containersOf(props.parsed));
const container = ref('');
const previous = ref(false);
const tail = ref<number>(DEFAULT_TAIL);
const follow = ref(true);
const matchCase = ref(false);
const query = ref('');
const reconnects = ref(0);

// Toolbar toggles are remembered as a convenience only.
const PREF_KEY = 'v8s:logs-prefs';
function loadPrefs(): { timestamps: boolean; wrap: boolean } {
    try {
        const p = JSON.parse(localStorage.getItem(PREF_KEY) ?? '{}') as { timestamps?: boolean; wrap?: boolean };
        return { timestamps: p.timestamps === true, wrap: p.wrap === true };
    } catch {
        return { timestamps: false, wrap: false };
    }
}
const prefs = loadPrefs();
const timestamps = ref(prefs.timestamps);
const wrap = ref(prefs.wrap);
watch([timestamps, wrap], () => {
    try {
        localStorage.setItem(PREF_KEY, JSON.stringify({ timestamps: timestamps.value, wrap: wrap.value }));
    } catch {
        // storage unavailable: the toggles just do not persist
    }
});

// Pods: default to the annotated / first non-init container once the object has loaded; keep a valid selection afterwards.
watch(
    containers,
    list => {
        if (!isPod.value || !list.length) return;
        if (!list.some(c => c.name === container.value)) container.value = defaultContainer(props.parsed, list);
    },
    { immediate: true }
);
const canPrevious = computed(() => isPod.value && hasPrevious(containers.value, container.value));
watch(canPrevious, ok => {
    if (!ok) previous.value = false;
});
// A different object starts fresh.
const targetKey = computed(() => `${props.target.cluster}|${props.target.gvr}|${props.target.namespace ?? ''}|${props.target.name ?? ''}`);
watch(targetKey, () => {
    container.value = '';
    previous.value = false;
    reconnects.value = 0;
    if (isPod.value) container.value = defaultContainer(props.parsed);
});

// ---- stream lifecycle ----

const handle = shallowRef<LogStreamHandle | null>(null);
const openError = ref('');
const opening = ref(false);
let token = 0;

function closeHandle() {
    handle.value?.close();
    handle.value = null;
}

async function open() {
    const my = ++token;
    closeHandle();
    openError.value = '';
    if (isPod.value && !container.value) {
        // Wait for the parsed pod; with no containers at all, let the agent pick.
        if (!props.parsed) return;
    }
    opening.value = true;
    const params = streamParams(props.target, {
        container: isPod.value ? container.value : undefined,
        follow: true,
        tailLines: tail.value,
        previous: previous.value,
        phone: isPhone.value
    });
    try {
        const h = await withWriteConfirm(props.target.cluster, extra => getAgent().openStream('logs', { ...params, ...extra }));
        if (my !== token) {
            h.close();
            return;
        }
        handle.value = h;
        follow.value = !previous.value;
        applySearch();
    } catch (e) {
        if (my === token) openError.value = errorText(e);
    } finally {
        if (my === token) opening.value = false;
    }
}

// Only these reopen the stream; timestamps / wrap / follow / match case never do.
const openKey = computed(
    () =>
        `${targetKey.value}|${isPod.value ? container.value : ''}|${previous.value}|${tail.value}|${reconnects.value}|${isPod.value && !props.parsed}`
);
watch(openKey, () => void open(), { immediate: true });
onBeforeUnmount(() => {
    token++;
    clearTimeout(timer);
    closeHandle();
});

// ---- search ----

let timer: ReturnType<typeof setTimeout> | undefined;
function applySearch() {
    handle.value?.searchFor(query.value, matchCase.value);
}
watch(query, () => {
    clearTimeout(timer);
    timer = setTimeout(applySearch, 150);
});
watch(matchCase, applySearch);

const search = computed(() => handle.value?.search.value);
const countText = computed(() => {
    const s = search.value;
    if (!query.value || !s) return '';
    return `${s.current} / ${s.count}`;
});
const step = (dir: 1 | -1) => {
    if (!query.value) return;
    // Flush a pending debounce so Enter right after typing searches the typed text.
    if (timer) {
        clearTimeout(timer);
        timer = undefined;
        applySearch();
    }
    handle.value?.searchStep(dir);
};
function onSearchKey(e: KeyboardEvent) {
    if (e.key === 'Enter') {
        e.preventDefault();
        step(e.shiftKey ? -1 : 1);
    }
}

// ---- actions ----

async function download() {
    const params = downloadParams(props.target, { container: isPod.value ? container.value : undefined, previous: previous.value });
    const filename = downloadFilename(props.target, isPod.value ? container.value : undefined);
    try {
        // The save picker needs the click's user activation: ask once, first, outside the write-confirm wrapper (its retry must not re-open it).
        const file = await getAgent().pickLogFile(filename);
        if (file === false) return;
        await withWriteConfirm(props.target.cluster, extra => getAgent().downloadLog({ ...params, ...extra }, filename, file));
    } catch (e) {
        ui.toast('danger', errorText(e));
    }
}
const clear = () => handle.value?.clear();
const reconnect = () => reconnects.value++;

// ---- status ----

const state = computed(() => handle.value?.state.value);
const writeTag = computed(() => {
    const used = handle.value?.usedContext;
    return used && used === clusters.writeContextOf(props.target.cluster) ? used : '';
});
const stateWord = computed(() => (opening.value ? 'connecting' : (state.value ?? '')));
const canReconnect = computed(() => state.value === 'ended' && !previous.value);
const statusText = computed(() => {
    if (opening.value) return 'Connecting';
    if (openError.value) return 'Error';
    if (state.value === 'live') return 'Live';
    if (state.value === 'ended') return 'Ended';
    if (state.value === 'error') return 'Error';
    return '';
});
const toneClass = computed(() => (openError.value || state.value === 'error' ? 'danger' : state.value === 'live' ? 'success' : ''));
const waiting = computed(() => isPod.value && !props.parsed && !handle.value && !openError.value);
</script>

<template>
    <div class="logs" data-testid="logs-pane">
        <div class="bar" role="toolbar" aria-label="Log controls">
            <label v-if="isPod && containers.length" class="sel">
                <span class="sr-only">Container</span>
                <select v-model="container" data-testid="logs-container" aria-label="Container">
                    <option v-for="c in containers" :key="c.name" :value="c.name">
                        {{ c.name }}{{ c.init ? ' (init)' : '' }}{{ c.ephemeral ? ' (debug)' : '' }}
                    </option>
                </select>
            </label>
            <span v-else-if="!isPod" class="muted all">All pods and containers, merged</span>

            <button type="button" class="btn sm tog" :aria-pressed="follow" data-testid="logs-follow" @click="follow = !follow">Follow</button>
            <button type="button" class="btn sm tog" :aria-pressed="timestamps" @click="timestamps = !timestamps">Timestamps</button>
            <button type="button" class="btn sm tog" :aria-pressed="wrap" title="Wrap shows at most two lines per entry" @click="wrap = !wrap">
                Wrap
            </button>
            <button
                v-if="isPod"
                type="button"
                class="btn sm tog"
                :aria-pressed="previous"
                :disabled="!canPrevious"
                title="Logs of the previous container instance"
                @click="previous = !previous"
            >
                Previous
            </button>

            <label class="sel">
                <span class="sr-only">Tail lines</span>
                <select v-model.number="tail" aria-label="Tail lines">
                    <option v-for="n in TAIL_OPTIONS" :key="n" :value="n">Last {{ n.toLocaleString('en-US') }}</option>
                </select>
            </label>

            <div class="field find">
                <input v-model="query" type="search" placeholder="Search" aria-label="Search logs" data-testid="logs-search" @keydown="onSearchKey" />
                <span class="count mono muted" data-testid="logs-count" aria-live="polite">{{ countText }}</span>
            </div>
            <button type="button" class="btn sm" aria-label="Previous match" :disabled="!query" data-testid="logs-prev" @click="step(-1)">
                Prev
            </button>
            <button type="button" class="btn sm" aria-label="Next match" :disabled="!query" data-testid="logs-next" @click="step(1)">Next</button>
            <button
                type="button"
                class="btn sm tog"
                :aria-pressed="matchCase"
                aria-label="Match case"
                title="Match case"
                @click="matchCase = !matchCase"
            >
                Aa
            </button>

            <span class="grow"></span>
            <button type="button" class="btn sm" data-testid="logs-download" @click="download">Download</button>
            <button type="button" class="btn sm" :disabled="!handle" @click="clear">Clear</button>
        </div>

        <div class="status" role="status">
            <span v-if="statusText" class="tag" :class="toneClass">{{ statusText }}</span>
            <span v-if="writeTag" class="tag release" :title="`Opened through write context ${writeTag}`">write</span>
            <span v-if="state === 'error' && handle?.endError.value" class="msg">{{ handle.endError.value }}</span>
            <button v-if="canReconnect" type="button" class="btn sm" @click="reconnect">Reconnect</button>
            <span v-if="previous" class="muted">Previous container instance</span>
            <span class="sr-only">{{ stateWord }}</span>
        </div>

        <p v-if="openError" class="notice danger" role="alert">
            <span>{{ openError }}</span>
            <button type="button" class="btn sm" @click="reconnect">Reconnect</button>
        </p>
        <p v-else-if="waiting" class="muted">Loading containers…</p>

        <LogViewer
            v-if="handle"
            :handle="handle"
            :follow="follow"
            :timestamps="timestamps"
            :wrap="wrap"
            :show-numbers="!isPhone"
            @update:follow="follow = $event"
            @download="download"
        />
    </div>
</template>

<style scoped>
.logs {
    display: flex;
    flex-direction: column;
    gap: 8px;
    /* The viewer is size-contained, so the pane needs a definite height; a parent can override it. */
    height: calc(100dvh - 230px);
    min-height: 320px;
}
.bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
}
.grow {
    flex: 1;
}
.all {
    font-size: 12px;
}
.sel select {
    height: 26px;
    border: 1px solid var(--bstrong);
    border-radius: 5px;
    background: var(--surface);
    color: var(--text);
    font-size: 12px;
    padding: 0 6px;
    max-width: 200px;
}
.tog[aria-pressed='true'] {
    border-color: var(--focus);
    background: var(--accent-soft);
}
.find {
    height: 26px;
    border-radius: 5px;
    min-width: 140px;
    flex: 0 1 220px;
}
.find input {
    font-size: 12px;
}
.count {
    font-size: 11px;
    white-space: nowrap;
}
.status {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 22px;
    font-size: 12px;
}
.msg {
    color: var(--danger);
}
.notice {
    justify-content: space-between;
    align-items: center;
}
@media (pointer: coarse) {
    .btn.sm,
    .sel select,
    .find {
        min-height: 44px;
    }
    .bar {
        gap: 8px;
    }
}
@media (max-width: 639.98px) {
    .logs {
        height: calc(100dvh - 190px);
    }
    .find {
        order: 10;
        flex: 1 1 100%;
    }
}
</style>
