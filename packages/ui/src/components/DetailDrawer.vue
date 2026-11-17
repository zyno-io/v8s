<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import type { DrawerTab } from '@/lib/route.ts';

import ActionBar from '@/components/ActionBar.vue';
import ObjectOverview from '@/components/ObjectOverview.vue';
import { kinds } from '@/lib/agent.ts';
import { clusterColor } from '@/lib/colors.ts';
import { closeDrawer, openObject } from '@/lib/drawer.ts';
import { kindLabel, labelFromGvr } from '@/lib/kinds.ts';
import { useObjectSession } from '@/lib/object-session.ts';
import { GITOPS_LABEL, objectStatus, revisionOf } from '@/lib/overview.ts';
import { DRAWER_TABS, gvrFromRoute, parseDrawer } from '@/lib/route.ts';

const YamlPane = defineAsyncComponent(() => import('@/components/YamlPane.vue'));
const EventsPane = defineAsyncComponent(() => import('@/components/EventsPane.vue'));
const RelatedPane = defineAsyncComponent(() => import('@/components/RelatedPane.vue'));

const route = useRoute();
const router = useRouter();

const drawer = computed(() => parseDrawer(gvrFromRoute(route.path, route.params), route.query));
const target = computed(() => drawer.value?.ref ?? null);
const tab = computed<DrawerTab>(() => drawer.value?.tab ?? 'overview');
const { payload, parsed, deleted, error, loading, frames } = useObjectSession(target);

const kind = computed(() => kinds.value.find(k => k.gvr === target.value?.gvr));
const kindName = computed(() => (kind.value ? kind.value.kind : target.value ? labelFromGvr(target.value.gvr) : ''));
const status = computed(() => (target.value ? objectStatus(target.value.gvr, parsed.value) : null));
const replicas = computed(() => Number((parsed.value?.spec as Record<string, unknown> | undefined)?.replicas ?? 1));
const revision = computed(() => revisionOf(parsed.value));
const actionTargets = computed(() => (target.value ? [target.value] : []));

function setTab(t: DrawerTab) {
    if (target.value) void openObject(router, target.value, t, true);
}
function onTabKey(e: KeyboardEvent) {
    const i = DRAWER_TABS.indexOf(tab.value);
    let n = i;
    if (e.key === 'ArrowRight') n = (i + 1) % DRAWER_TABS.length;
    else if (e.key === 'ArrowLeft') n = (i + DRAWER_TABS.length - 1) % DRAWER_TABS.length;
    else if (e.key === 'Home') n = 0;
    else if (e.key === 'End') n = DRAWER_TABS.length - 1;
    else return;
    e.preventDefault();
    setTab(DRAWER_TABS[n]);
    void nextTick(() => document.getElementById(`tab-${DRAWER_TABS[n]}`)?.focus());
}

function openSource(kindStr: string, name: string, ns: string | undefined) {
    if (!target.value) return;
    const gvr = kinds.value.find(k => k.kind === kindStr)?.gvr;
    if (gvr) void openObject(router, { cluster: target.value.cluster, gvr, namespace: ns, name });
}

// ---- focus and width ----
const closeBtn = ref<HTMLButtonElement | null>(null);
watch(
    () => target.value && `${target.value.cluster}|${target.value.gvr}|${target.value.namespace}|${target.value.name}`,
    async k => {
        if (k) {
            await nextTick();
            closeBtn.value?.focus();
        }
    },
    { immediate: true }
);

function loadWidth(): number {
    try {
        const v = Number(localStorage.getItem('v8s:drawer-w'));
        if (v >= 420 && v <= 1100) return v;
    } catch {
        // ignore
    }
    return 600;
}
const width = ref(loadWidth());
function startResize(e: PointerEvent) {
    e.preventDefault();
    const startX = e.clientX;
    const startW = width.value;
    const move = (ev: PointerEvent) => (width.value = Math.max(420, Math.min(Math.round(window.innerWidth * 0.8), startW + startX - ev.clientX)));
    const up = () => {
        window.removeEventListener('pointermove', move);
        window.removeEventListener('pointerup', up);
        try {
            localStorage.setItem('v8s:drawer-w', String(width.value));
        } catch {
            // ignore
        }
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
}
function resizeKey(e: KeyboardEvent) {
    if (e.key === 'ArrowLeft') width.value = Math.min(Math.round(window.innerWidth * 0.8), width.value + 24);
    else if (e.key === 'ArrowRight') width.value = Math.max(420, width.value - 24);
}

const TAB_LABEL: Record<DrawerTab, string> = { overview: 'Overview', yaml: 'YAML', events: 'Events', related: 'Related' };
</script>

<template>
    <aside
        v-if="target"
        class="drawer floating"
        role="complementary"
        :aria-label="`${kindName} ${target.name}`"
        :style="{ '--dw': width + 'px' }"
        data-testid="drawer"
    >
        <div
            class="grip"
            role="separator"
            aria-orientation="vertical"
            tabindex="0"
            aria-label="Resize details panel"
            @pointerdown="startResize"
            @keydown="resizeKey"
        ></div>
        <header>
            <div class="meta">
                <span class="tag"><span class="dot" :style="{ background: clusterColor(target.cluster) }"></span>{{ target.cluster }}</span>
                <span class="muted small">{{ kindName }}<template v-if="target.namespace"> · {{ target.namespace }}</template></span>
                <div class="grow"></div>
                <ActionBar
                    v-if="payload && !deleted"
                    :gvr="target.gvr"
                    :targets="actionTargets"
                    :kind-label="kindName"
                    variant="menu"
                    :current-replicas="replicas"
                    :current-revision="revision"
                />
                <button
                    ref="closeBtn"
                    class="btn ghost sm"
                    type="button"
                    aria-label="Close details"
                    data-testid="drawer-close"
                    @click="closeDrawer(router)"
                >
                    <svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                        <path d="M4 4l8 8M12 4l-8 8" />
                    </svg>
                </button>
            </div>
            <div class="title">
                <h2 class="c">{{ target.name }}</h2>
                <span v-if="status" class="tag" :class="status.tone">{{ status.text }}</span>
                <span v-if="payload?.gitops" class="tag">managed by {{ GITOPS_LABEL[payload.gitops.kind] ?? payload.gitops.kind }}</span>
            </div>
        </header>
        <div class="tabs" role="tablist" aria-label="Object sections" @keydown="onTabKey">
            <button
                v-for="t in DRAWER_TABS"
                :id="`tab-${t}`"
                :key="t"
                class="tab"
                :class="{ on: tab === t }"
                type="button"
                role="tab"
                :aria-selected="tab === t"
                :aria-controls="`panel-${t}`"
                :tabindex="tab === t ? 0 : -1"
                @click="setTab(t)"
            >
                {{ TAB_LABEL[t] }}
            </button>
        </div>
        <div :id="`panel-${tab}`" class="panel" role="tabpanel" :aria-labelledby="`tab-${tab}`">
            <p v-if="error && !payload" class="notice danger" role="alert">{{ error }}</p>
            <p v-else-if="loading && !payload" class="muted" role="status">Loading…</p>
            <template v-else-if="payload">
                <div v-if="deleted && tab !== 'yaml'" class="notice danger" role="alert"><b>This object was deleted on the server.</b></div>
                <ObjectOverview v-if="tab === 'overview'" :target="target" :payload="payload" :parsed="parsed" @open-source="openSource" />
                <YamlPane v-else-if="tab === 'yaml'" :target="target" :payload="payload" :deleted="deleted" :frames="frames" />
                <EventsPane v-else-if="tab === 'events'" :key="`${target.cluster}${target.namespace}${target.name}`" :target="target" />
                <RelatedPane v-else :key="`${target.cluster}${target.namespace}${target.name}`" :target="target" :payload="payload" />
            </template>
        </div>
    </aside>
</template>

<style scoped>
.drawer {
    position: fixed;
    top: 46px;
    right: 0;
    bottom: 0;
    width: var(--dw, 600px);
    max-width: 100vw;
    z-index: 25;
    display: flex;
    flex-direction: column;
    border-radius: 0;
    border-width: 0 0 0 1px;
    overflow: visible;
}
.grip {
    position: absolute;
    left: -4px;
    top: 0;
    bottom: 0;
    width: 8px;
    cursor: col-resize;
    touch-action: none;
}
.grip:hover,
.grip:focus-visible {
    background: var(--accent-soft);
}
header {
    padding: 14px 16px 8px;
    display: flex;
    flex-direction: column;
    gap: 6px;
}
.meta,
.title {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
}
.small {
    font-size: 12px;
}
h2 {
    margin: 0;
    font-size: 17px;
    font-weight: 600;
    min-width: 0;
}
.tabs {
    display: flex;
    gap: 2px;
    padding: 0 12px;
    border-bottom: 1px solid var(--border);
    overflow-x: auto;
}
.tab {
    all: unset;
    box-sizing: border-box;
    padding: 8px 10px;
    font-size: 13px;
    font-weight: 500;
    color: var(--text2);
    cursor: pointer;
    border-bottom: 2px solid transparent;
    white-space: nowrap;
}
.tab:hover {
    color: var(--text);
}
.tab.on {
    color: var(--text);
    border-bottom-color: var(--accent);
}
.tab:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.panel {
    flex: 1 1 0;
    min-height: 0;
    overflow: auto;
    padding: 14px 16px;
    display: flex;
    flex-direction: column;
    gap: 12px;
}
.panel > :deep(*) {
    flex: none;
}
.panel > :deep(.ypane) {
    flex: 1 1 0;
}
@media (max-width: 899.98px) {
    .drawer {
        width: 100vw;
        top: 0;
    }
    .grip {
        display: none;
    }
}
</style>
