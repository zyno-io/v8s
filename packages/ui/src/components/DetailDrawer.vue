<script setup lang="ts">
import type { ObjectPayload } from '@v8s/client';

import { computed, defineAsyncComponent, nextTick, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import type { DrawerTab } from '@/lib/route.ts';

import ActionBar from '@/components/ActionBar.vue';
import ObjectOverview from '@/components/ObjectOverview.vue';
import ProtectedMark from '@/components/ProtectedMark.vue';
import { kinds } from '@/lib/agent.ts';
import { clusterColor } from '@/lib/colors.ts';
import { closeDrawer, openObject } from '@/lib/drawer.ts';
import { kindLabel, labelFromGvr, resolveKind } from '@/lib/kinds.ts';
import { useObjectSession } from '@/lib/object-session.ts';
import { GITOPS_LABEL, objectStatus, revisionOf } from '@/lib/overview.ts';
import { gvrFromRoute, HELM_RELEASES_GVR, parseDrawer, tabsFor } from '@/lib/route.ts';
import { useIntegrationsStore } from '@/stores/integrations.ts';

const YamlPane = defineAsyncComponent(() => import('@/components/YamlPane.vue'));
const EventsPane = defineAsyncComponent(() => import('@/components/EventsPane.vue'));
const RelatedPane = defineAsyncComponent(() => import('@/components/RelatedPane.vue'));
const SecretPane = defineAsyncComponent(() => import('@/components/SecretPane.vue'));
const LogsPane = defineAsyncComponent(() => import('@/components/LogsPane.vue'));
const ShellPane = defineAsyncComponent(() => import('@/components/ShellPane.vue'));
const FluxInventory = defineAsyncComponent(() => import('@/components/FluxInventory.vue'));
const HelmReleaseFlux = defineAsyncComponent(() => import('@/components/HelmReleaseFlux.vue'));
const HelmReleaseDrawer = defineAsyncComponent(() => import('@/components/HelmReleaseDrawer.vue'));
const CveTable = defineAsyncComponent(() => import('@/components/CveTable.vue'));
const SecurityPane = defineAsyncComponent(() => import('@/components/SecurityPane.vue'));
const FlowsPane = defineAsyncComponent(() => import('@/components/FlowsPane.vue'));

const route = useRoute();
const router = useRouter();

const drawer = computed(() => parseDrawer(gvrFromRoute(route.path, route.params), route.query));
const target = computed(() => drawer.value?.ref ?? null);
const tab = computed<DrawerTab>(() => drawer.value?.tab ?? 'overview');
const isSecret = computed(() => target.value?.gvr === 'v1/secrets');
// A Secret is never subscribed on open: the agent would stream every value. Its YAML tab does a masked `object.get` on demand.
const isHelm = computed(() => target.value?.gvr === HELM_RELEASES_GVR);
// Helm releases are virtual (no object to subscribe to); their drawer loads through `helm.release`.
const sessionTarget = computed(() => (isSecret.value || isHelm.value ? null : target.value));
const integrations = useIntegrationsStore();
const tabs = computed(() =>
    target.value
        ? tabsFor(target.value.gvr, {
              security: integrations.detected.security,
              // Flows exist only in clusters with a reachable relay; a namespace or workload of another cluster has none.
              flows: integrations.hubbleClusters.includes(target.value.cluster)
          })
        : []
);
const { payload, parsed, deleted, error, loading, frames } = useObjectSession(sessionTarget);
const EMPTY_PAYLOAD: ObjectPayload = Object.freeze({ yaml: '', uid: '', resourceVersion: '', managedBy: [], owners: [] }) as ObjectPayload;

const kind = computed(() => kinds.value.find(k => k.gvr === target.value?.gvr));
const kindName = computed(() => (kind.value ? kind.value.kind : target.value ? labelFromGvr(target.value.gvr) : ''));
const status = computed(() => (target.value ? objectStatus(target.value.gvr, parsed.value) : null));
/** Undefined when unknown (not parsed yet, or too large to parse): the Scale dialog then asks instead of guessing. */
const replicas = computed(() => {
    const r = (parsed.value?.spec as Record<string, unknown> | undefined)?.replicas;
    return typeof r === 'number' && Number.isFinite(r) ? r : undefined;
});
const revision = computed(() => revisionOf(parsed.value));
// Pin actions to the incarnation the drawer shows: the live payload's uid travels with every action/delete/apply ref.
// (`liveUid` is a separate computed so `pinned` keeps its identity across payload frames: ActionBar re-checks access when its targets change.)
const liveUid = computed(() => payload.value?.uid || undefined);
const pinned = computed(() => (target.value ? { ...target.value, uid: liveUid.value } : null));
const actionTargets = computed(() => (pinned.value ? [pinned.value] : []));

function setTab(t: DrawerTab) {
    if (target.value) void openObject(router, target.value, t, true);
}
function onTabKey(e: KeyboardEvent) {
    const list = tabs.value;
    const i = list.indexOf(tab.value);
    let n = i;
    if (e.key === 'ArrowRight') n = (i + 1) % list.length;
    else if (e.key === 'ArrowLeft') n = (i + list.length - 1) % list.length;
    else if (e.key === 'Home') n = 0;
    else if (e.key === 'End') n = list.length - 1;
    else return;
    e.preventDefault();
    setTab(list[n]);
    void nextTick(() => document.getElementById(`tab-${list[n]}`)?.focus());
}

const SOURCE_GROUP: Record<string, string> = { Kustomization: 'kustomize.toolkit.fluxcd.io', HelmRelease: 'helm.toolkit.fluxcd.io' };
function openSource(kindStr: string, name: string, ns: string | undefined) {
    if (!target.value) return;
    const gvr = resolveKind(kinds.value, kindStr, SOURCE_GROUP[kindStr])?.gvr;
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

const TAB_LABEL: Record<DrawerTab, string> = {
    overview: 'Overview',
    logs: 'Logs',
    shell: 'Shell',
    inventory: 'Inventory',
    release: 'Release',
    report: 'Report',
    security: 'Security',
    flows: 'Flows',
    yaml: 'YAML',
    events: 'Events',
    related: 'Related'
};
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
                <span class="tag"
                    ><span class="dot" :style="{ background: clusterColor(target.cluster) }"></span>{{ target.cluster
                    }}<ProtectedMark :cluster="target.cluster"
                /></span>
                <span class="muted small">{{ kindName }}<template v-if="target.namespace"> · {{ target.namespace }}</template></span>
                <div class="grow"></div>
                <ActionBar
                    v-if="(payload || isSecret) && !deleted && !isHelm"
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
        <HelmReleaseDrawer v-if="isHelm" :key="`${target.cluster}${target.namespace}${target.name}`" :target="target" class="helmwrap" />
        <div v-if="!isHelm" class="tabs" role="tablist" aria-label="Object sections" @keydown="onTabKey">
            <button
                v-for="t in tabs"
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
        <div v-if="!isHelm" :id="`panel-${tab}`" class="panel" role="tabpanel" :aria-labelledby="`tab-${tab}`">
            <template v-if="isSecret">
                <div v-if="tab === 'overview'" class="notice" role="note" data-testid="secret-overview">
                    <div>
                        <b>Secret values are not loaded.</b> Nothing is fetched for a Secret until you ask. Open the YAML tab for the masked manifest;
                        values stay hidden until you choose Reveal.
                        <dl class="sgrid">
                            <dt class="lbl">Cluster</dt>
                            <dd>{{ target.cluster }}</dd>
                            <dt v-if="target.namespace" class="lbl">Namespace</dt>
                            <dd v-if="target.namespace">{{ target.namespace }}</dd>
                            <dt class="lbl">Name</dt>
                            <dd class="mono">{{ target.name }}</dd>
                        </dl>
                        <div class="srow"><button class="btn sm" type="button" @click="setTab('yaml')">Open YAML (masked)</button></div>
                    </div>
                </div>
                <SecretPane v-else-if="tab === 'yaml'" :key="`${target.cluster}${target.namespace}${target.name}`" :target="target" />
                <EventsPane v-else-if="tab === 'events'" :key="`${target.cluster}${target.gvr}${target.namespace}${target.name}`" :target="target" />
                <RelatedPane v-else :key="`${target.cluster}${target.namespace}${target.name}`" :target="target" :payload="EMPTY_PAYLOAD" />
            </template>
            <p v-else-if="error && !payload" class="notice danger" role="alert">{{ error }}</p>
            <p v-else-if="loading && !payload" class="muted" role="status">Loading…</p>
            <template v-else-if="payload">
                <div v-if="deleted && tab !== 'yaml'" class="notice danger" role="alert"><b>This object was deleted on the server.</b></div>
                <ObjectOverview v-if="tab === 'overview'" :target="target" :payload="payload" :parsed="parsed" @open-source="openSource" />
                <LogsPane v-else-if="tab === 'logs'" :key="`${target.cluster}${target.namespace}${target.name}`" :target="target" :parsed="parsed" />
                <ShellPane
                    v-else-if="tab === 'shell'"
                    :key="`${target.cluster}${target.namespace}${target.name}`"
                    :target="target"
                    :parsed="parsed"
                />
                <FluxInventory
                    v-else-if="tab === 'inventory'"
                    :key="`${target.cluster}${target.namespace}${target.name}`"
                    :target="target"
                    :refresh-key="frames"
                />
                <HelmReleaseFlux v-else-if="tab === 'release'" :target="target" :parsed="parsed" />
                <CveTable v-else-if="tab === 'report'" :key="`${target.cluster}${target.namespace}${target.name}`" :target="pinned ?? target" />
                <SecurityPane
                    v-else-if="tab === 'security'"
                    :key="`${target.cluster}${target.namespace}${target.name}`"
                    :target="target"
                    :parsed="parsed"
                />
                <FlowsPane
                    v-else-if="tab === 'flows'"
                    :key="`${target.cluster}${target.namespace}${target.name}`"
                    :target="target"
                    :parsed="parsed"
                />
                <YamlPane v-else-if="tab === 'yaml'" :target="pinned ?? target" :payload="payload" :deleted="deleted" :frames="frames" />
                <EventsPane v-else-if="tab === 'events'" :key="`${target.cluster}${target.gvr}${target.namespace}${target.name}`" :target="target" />
                <RelatedPane v-else :key="`${target.cluster}${target.namespace}${target.name}`" :target="target" :payload="payload" />
            </template>
        </div>
    </aside>
</template>

<style scoped>
.sgrid {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 4px 12px;
    margin: 8px 0;
}
.sgrid dd {
    margin: 0;
}
.srow {
    margin-top: 6px;
}
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
.panel > :deep([data-testid='logs-pane']),
.panel > :deep([data-testid='shell-pane']),
.panel > :deep([data-testid='flux-inventory']),
.panel > :deep([data-testid='flows-pane']) {
    flex: 1 1 0;
    height: auto;
    min-height: 320px;
}
.helmwrap {
    flex: 1 1 0;
    min-height: 0;
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
