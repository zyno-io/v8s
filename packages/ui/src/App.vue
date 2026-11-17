<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, watch } from 'vue';
import { useRoute } from 'vue-router';

import ClusterSelector from './components/ClusterSelector.vue';
import CommandBar from './components/CommandBar.vue';
import DialogHost from './components/DialogHost.vue';
import DrainHost from './components/DrainHost.vue';
import NodeShellHost from './components/NodeShellHost.vue';
import PortForwardHost from './components/PortForwardHost.vue';
import ShortcutsOverlay from './components/ShortcutsOverlay.vue';
import SideNav from './components/SideNav.vue';
import Toaster from './components/Toaster.vue';
import TopBar from './components/TopBar.vue';
import { bootMessage, bootState, getAgent, refreshKinds, startAgent } from './lib/agent.ts';
import { installGlobalKeys } from './lib/keys.ts';
import { useClustersStore } from './stores/clusters.ts';
import { useSettingsStore } from './stores/settings.ts';
import { useUiStore } from './stores/ui.ts';

// Heavy surfaces load on first use: the drawer (CodeMirror, YAML, diff), the palette and the create dialog.
const DetailDrawer = defineAsyncComponent(() => import('./components/DetailDrawer.vue'));
const SearchPalette = defineAsyncComponent(() => import('./components/SearchPalette.vue'));
const CreateDialog = defineAsyncComponent(() => import('./components/CreateDialog.vue'));

const clusters = useClustersStore();
const settings = useSettingsStore();
const ui = useUiStore();
const route = useRoute();
const drawerOpen = computed(() => !!(route.query.c && route.query.n));
const closed = computed(() => bootState.value === 'ready' && getAgent().status.value === 'closed');
let uninstall: (() => void) | null = null;
const reload = () => location.reload();

async function boot() {
    await startAgent();
    if (bootState.value === 'ready') {
        try {
            await settings.load();
            await clusters.load();
        } catch (e) {
            bootMessage.value = e instanceof Error ? e.message : String(e);
            bootState.value = 'error';
        }
    }
}

// A cluster finishing discovery changes the union of kinds; re-read `kinds.list` when the set of phases changes.
watch(
    () => clusters.states.map(s => s.id + s.phase).join(),
    () => refreshKinds()
);

onMounted(() => {
    uninstall = installGlobalKeys(() => document.getElementById('pods-filter')?.focus());
    void boot();
});
onBeforeUnmount(() => uninstall?.());
</script>

<template>
    <div v-if="bootState === 'ready'" class="frame">
        <TopBar />
        <div v-if="closed" class="banner" role="alert">
            Disconnected from the agent. <button class="btn sm" type="button" @click="reload">Reload</button>
        </div>
        <div class="body">
            <SideNav />
            <div class="content">
                <router-view v-slot="{ Component, route: r }"><component :is="Component" :key="r.path" /></router-view>
                <CommandBar />
            </div>
        </div>
        <ClusterSelector />
        <ShortcutsOverlay />
        <DetailDrawer v-if="drawerOpen" />
        <SearchPalette v-if="ui.paletteOpen" />
        <CreateDialog v-if="ui.createOpen" />
        <DialogHost />
        <DrainHost />
        <PortForwardHost />
        <NodeShellHost />
        <Toaster />
    </div>
    <div v-else class="splash" role="status">
        <div class="mark" aria-hidden="true">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M3 3l5 10 5-10" /></svg>
        </div>
        <template v-if="bootState === 'booting'">
            <p>Connecting to the v8s agent…</p>
        </template>
        <template v-else-if="bootState === 'no-endpoint'">
            <p><b>No agent endpoint.</b></p>
            <p class="muted">
                Open this page with <span class="mono">#agent=ws://127.0.0.1:PORT/ws&amp;token=…</span> or run inside the v8s desktop app.
            </p>
        </template>
        <template v-else>
            <p><b>Could not connect.</b></p>
            <p class="muted">{{ bootMessage }}</p>
            <button class="btn" type="button" @click="boot">Retry</button>
        </template>
    </div>
</template>

<style scoped>
.frame {
    height: 100%;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    position: relative;
}
.body {
    flex: 1 1 0;
    display: flex;
    min-height: 0;
}
.content {
    flex-grow: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    position: relative;
}
.banner {
    background: var(--danger-soft);
    border-bottom: 1px solid var(--danger-b);
    color: var(--danger);
    padding: 6px 16px;
    font-size: 13px;
    display: flex;
    gap: 10px;
    align-items: center;
}
.splash {
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 6px;
    text-align: center;
    padding: 0 24px;
}
.splash p {
    margin: 0;
}
@media (max-width: 899.98px) {
    .body :deep(.side) {
        display: none;
    }
}
</style>
