<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted } from 'vue';

import ClusterSelector from './components/ClusterSelector.vue';
import CommandBar from './components/CommandBar.vue';
import DetailDrawer from './components/DetailDrawer.vue';
import ShortcutsOverlay from './components/ShortcutsOverlay.vue';
import SideNav from './components/SideNav.vue';
import TopBar from './components/TopBar.vue';
import { bootMessage, bootState, getAgent, startAgent } from './lib/agent.ts';
import { installGlobalKeys } from './lib/keys.ts';
import { useClustersStore } from './stores/clusters.ts';

const clusters = useClustersStore();
const closed = computed(() => bootState.value === 'ready' && getAgent().status.value === 'closed');
let uninstall: (() => void) | null = null;
const reload = () => location.reload();

async function boot() {
    await startAgent();
    if (bootState.value === 'ready') {
        try {
            await clusters.load();
        } catch (e) {
            bootMessage.value = e instanceof Error ? e.message : String(e);
            bootState.value = 'error';
        }
    }
}

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
                <router-view />
                <CommandBar />
            </div>
        </div>
        <ClusterSelector />
        <ShortcutsOverlay />
        <DetailDrawer />
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
