<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useRoute } from 'vue-router';

import BottomNav from './components/BottomNav.vue';
import ClusterSelector from './components/ClusterSelector.vue';
import CommandBar from './components/CommandBar.vue';
import DialogHost from './components/DialogHost.vue';
import DrainHost from './components/DrainHost.vue';
import NodeShellHost from './components/NodeShellHost.vue';
import PortForwardHost from './components/PortForwardHost.vue';
import ShortcutsOverlay from './components/ShortcutsOverlay.vue';
import SideBar from './components/SideBar.vue';
import Toaster from './components/Toaster.vue';
import TopBar from './components/TopBar.vue';
import { bootMessage, bootState, getAgent, refreshKinds, startAgent } from './lib/agent.ts';
import { clusterLoginUrl, hostedMe, loginUrl, logoutState, sessionEnded } from './lib/hosted.ts';
import { installGlobalKeys } from './lib/keys.ts';
import { useClustersStore } from './stores/clusters.ts';
import { useIntegrationsStore } from './stores/integrations.ts';
import { useSettingsStore } from './stores/settings.ts';
import { useUiStore } from './stores/ui.ts';

// Heavy surfaces load on first use: the drawer (CodeMirror, YAML, diff), the palette and the create dialog.
const DetailDrawer = defineAsyncComponent(() => import('./components/DetailDrawer.vue'));
const SearchPalette = defineAsyncComponent(() => import('./components/SearchPalette.vue'));
const CreateDialog = defineAsyncComponent(() => import('./components/CreateDialog.vue'));

const clusters = useClustersStore();
const settings = useSettingsStore();
const ui = useUiStore();
const integrations = useIntegrationsStore();
const route = useRoute();
const drawerOpen = computed(() => !!(route.query.c && route.query.n));
const closed = computed(() => bootState.value === 'ready' && getAgent().status.value === 'closed');
let uninstall: (() => void) | null = null;
const reload = () => location.reload();

// Connection loss: the client reconnects by itself and the views keep their last rows. Show a calm "Reconnecting…" only after a
// short grace (quick blips stay invisible), and escalate to "Can't reach the agent" after a minute of failing.
const RECONNECT_NOTICE_MS = 1500;
const RECONNECT_GIVE_UP_MS = 60_000;
const reconnecting = computed(() => bootState.value === 'ready' && getAgent().status.value === 'reconnecting');
const showReconnecting = ref(false);
const unreachable = ref(false);
let noticeTimer: ReturnType<typeof setTimeout> | undefined;
let giveUpTimer: ReturnType<typeof setTimeout> | undefined;
watch(reconnecting, on => {
    clearTimeout(noticeTimer);
    clearTimeout(giveUpTimer);
    if (!on) {
        showReconnecting.value = false;
        unreachable.value = false;
        return;
    }
    noticeTimer = setTimeout(() => (showReconnecting.value = true), RECONNECT_NOTICE_MS);
    giveUpTimer = setTimeout(() => (unreachable.value = true), RECONNECT_GIVE_UP_MS);
});
const retryNow = () => getAgent().reconnectNow();

// Back after a drop: the views are resubscribed by the client; re-read what is derived from the connection (kinds, cluster selection).
watch(
    () => (bootState.value === 'ready' ? getAgent().reconnects.value : 0),
    n => {
        if (!n) return;
        refreshKinds(0);
        void clusters.restoreAfterReconnect().catch(() => {});
    }
);

// Hosted: a cluster whose app login lapsed (`auth-required`), or `/api/me` saying `clusterAuth: required`, needs the cluster-login flow.
const needsClusterLogin = computed(
    () => !!hostedMe.value && (hostedMe.value.clusterAuth === 'required' || clusters.states.some(s => s.phase === 'auth-required'))
);
const returnPath = () => location.pathname + location.search;
const signIn = () => {
    try {
        sessionStorage.removeItem('v8s.loginAt');
    } catch {
        // no storage
    }
    location.assign(loginUrl('/'));
};
const clusterLogin = () => location.assign(clusterLoginUrl(returnPath()));

// A hosted socket that closes may mean the session ended (expiry, eviction, logout elsewhere): then sign in again.
watch(closed, async isClosed => {
    if (isClosed && hostedMe.value && !logoutState.active && (await sessionEnded(fetch.bind(globalThis))) && !logoutState.active)
        location.assign(loginUrl(returnPath()));
});

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
    () => {
        refreshKinds();
        void integrations.refreshHubble();
    }
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
        <div v-if="unreachable" class="banner warn" role="status" data-testid="agent-unreachable">
            Can't reach the agent.
            <button class="btn sm" type="button" @click="retryNow">Retry now</button>
            <button class="btn sm" type="button" @click="reload">Reload</button>
        </div>
        <div v-else-if="showReconnecting" class="banner calm" role="status" data-testid="agent-reconnecting">Reconnecting…</div>
        <div v-if="closed" class="banner" role="alert">
            Disconnected from the agent. <button class="btn sm" type="button" @click="reload">Reload</button>
        </div>
        <div v-if="needsClusterLogin" class="banner warn" role="alert" data-testid="cluster-auth-banner">
            Sign in to the clusters to continue. <button class="btn sm" type="button" @click="clusterLogin">Sign in to clusters</button>
        </div>
        <div class="body">
            <SideBar />
            <div class="content">
                <router-view v-slot="{ Component, route: r }"><component :is="Component" :key="r.path" /></router-view>
                <CommandBar />
            </div>
        </div>
        <BottomNav />
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
        <template v-else-if="bootState === 'signed-out'">
            <p><b>You have been signed out.</b></p>
            <button class="btn primary" type="button" data-testid="sign-in" @click="signIn">Sign in</button>
        </template>
        <template v-else-if="bootState === 'login-loop'">
            <p><b>Sign-in did not complete.</b></p>
            <p class="muted">The session cookie was not kept. Check that cookies are allowed for this site, then try again.</p>
            <button class="btn" type="button" data-testid="sign-in" @click="signIn">Try again</button>
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
.banner.calm {
    background: var(--neutral-soft);
    border-bottom-color: var(--neutral-b);
    color: var(--text2);
}
.banner.warn {
    background: var(--warning-soft);
    border-bottom-color: var(--warning-b);
    color: var(--warning);
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
</style>
