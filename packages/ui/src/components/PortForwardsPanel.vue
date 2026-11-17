<script setup lang="ts">
import type { PortForwardInfo, SavedForward } from '@v8s/client';

import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue';

import { errorText } from '@/lib/mutations.ts';
import {
    browserUrl,
    describeState,
    listForwards,
    portForwardsOpen,
    removeSavedForward,
    savedMatches,
    startForward,
    stopForward
} from '@/lib/portforward.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useUiStore } from '@/stores/ui.ts';

const ui = useUiStore();
const settings = useSettingsStore();
const el = ref<HTMLElement | null>(null);
const forwards = shallowRef<readonly PortForwardInfo[]>([]);
const err = ref('');
const loaded = ref(false);
const saved = computed<readonly SavedForward[]>(() => settings.doc.portForwards ?? []);
let timer: ReturnType<typeof setInterval> | undefined;
let returnFocus: HTMLElement | null = null;

async function refresh() {
    try {
        forwards.value = await listForwards();
        err.value = '';
    } catch (e) {
        err.value = errorText(e);
    }
    loaded.value = true;
}
function poll() {
    if (!document.hidden) void refresh();
}
function onVis() {
    if (!document.hidden) void refresh();
}
function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
        e.stopPropagation();
        close();
    }
}
function close() {
    portForwardsOpen.value = false;
}

onMounted(async () => {
    returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    void refresh();
    timer = setInterval(poll, 3000);
    document.addEventListener('visibilitychange', onVis);
    await nextTick();
    el.value?.focus();
});
onBeforeUnmount(() => {
    clearInterval(timer);
    document.removeEventListener('visibilitychange', onVis);
    returnFocus?.focus?.();
});

const targetText = (f: PortForwardInfo) => {
    const ns = f.ref.namespace ? `${f.ref.namespace}/` : '';
    return f.ref.gvr === 'v1/services' ? `${ns}${f.pod || f.ref.name}:${f.port}` : `${ns}${f.ref.name}:${f.port}`;
};
const savedText = (s: SavedForward) => s.name || `${s.ref.namespace ? s.ref.namespace + '/' : ''}${s.ref.name}:${s.port}`;

async function stop(f: PortForwardInfo) {
    try {
        await stopForward(f.id);
    } catch (e) {
        ui.toast('danger', errorText(e));
    }
    await refresh();
}
async function startSaved(s: SavedForward) {
    try {
        const r = await startForward(s.ref, s.port);
        ui.toast('success', `Forwarding on 127.0.0.1:${r.localPort}`);
    } catch (e) {
        ui.toast('danger', errorText(e));
    }
    await refresh();
}
async function removeSaved(i: number) {
    try {
        await removeSavedForward(i);
    } catch (e) {
        ui.toast('danger', errorText(e));
    }
}
</script>

<template>
    <section ref="el" class="pfp floating" role="dialog" aria-label="Port forwards" tabindex="-1" data-testid="pf-panel" @keydown="onKey">
        <header>
            <h2>Port forwards</h2>
            <button class="btn sm ghost" type="button" aria-label="Close port forwards" @click="close">Close</button>
        </header>
        <p v-if="err" class="err" role="alert">{{ err }}</p>
        <div aria-live="polite">
            <p v-if="loaded && !forwards.length" class="muted">No active forwards. Start one from a pod or service.</p>
            <ul v-else class="list">
                <li v-for="f in forwards" :key="f.id" class="row">
                    <span class="tag" :class="describeState(f.state).tone">{{ describeState(f.state).text }}</span>
                    <span class="mono">127.0.0.1:{{ f.localPort }}</span>
                    <span class="tgt mono"
                        >{{ targetText(f) }}<span v-if="f.ref.gvr === 'v1/services'" class="muted"> (svc {{ f.ref.name }})</span></span
                    >
                    <span class="acts">
                        <a class="btn sm" :href="browserUrl(f.localPort)" target="_blank" rel="noopener">Open in browser</a>
                        <button class="btn sm" type="button" :data-testid="`pf-stop-${f.id}`" @click="stop(f)">Stop</button>
                    </span>
                    <span v-if="f.error" class="err full">{{ f.error }}</span>
                </li>
            </ul>
        </div>
        <h3>Saved forwards</h3>
        <p v-if="!saved.length" class="muted">No saved forwards. Tick "Save this forward" when starting one.</p>
        <ul v-else class="list">
            <li v-for="(s, i) in saved" :key="i" class="row">
                <span class="tgt mono">{{ savedText(s) }}</span>
                <span class="muted mono">{{ s.ref.name }}:{{ s.port }}</span>
                <span v-if="savedMatches(s, forwards)" class="tag success">active</span>
                <span class="acts">
                    <button
                        class="btn sm"
                        type="button"
                        :disabled="!!savedMatches(s, forwards)"
                        :data-testid="`pf-saved-start-${i}`"
                        @click="startSaved(s)"
                    >
                        Start
                    </button>
                    <button class="btn sm ghost" type="button" :aria-label="`Remove saved forward ${savedText(s)}`" @click="removeSaved(i)">
                        Remove
                    </button>
                </span>
            </li>
        </ul>
    </section>
</template>

<style scoped>
.pfp {
    position: fixed;
    top: 52px;
    right: 12px;
    width: min(460px, calc(100vw - 24px));
    max-height: calc(100vh - 68px);
    overflow: auto;
    padding: 12px 14px;
    z-index: 60;
    color: var(--text);
    display: flex;
    flex-direction: column;
    gap: 8px;
}
header {
    display: flex;
    justify-content: space-between;
    align-items: center;
}
h2 {
    margin: 0;
    font-size: 15px;
}
h3 {
    margin: 6px 0 0;
    font-size: 13px;
    color: var(--text2);
}
p {
    margin: 0;
    font-size: 13px;
}
.list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
}
.row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 8px;
    font-size: 12px;
}
.tgt {
    min-width: 0;
    word-break: break-all;
}
.acts {
    margin-left: auto;
    display: flex;
    gap: 6px;
}
.full {
    flex-basis: 100%;
}
.err {
    color: var(--danger, #c0392b);
}
@media (max-width: 639px) {
    .pfp {
        left: 0;
        right: 0;
        width: auto;
        top: 48px;
        max-height: calc(100vh - 48px);
        border-radius: 12px 12px 0 0;
    }
}
@media (pointer: coarse) {
    .btn {
        min-height: 44px;
    }
}
</style>
