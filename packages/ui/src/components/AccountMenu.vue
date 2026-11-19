<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue';

import { getAgent } from '@/lib/agent.ts';
import { expiryMs, hostedMe, logout, sessionLeft } from '@/lib/hosted.ts';
import { versionTitle } from '@/lib/update.ts';

const agentInfo = getAgent().agentInfo;

const open = ref(false);
const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | undefined;

const me = computed(() => hostedMe.value);
const initials = computed(() => (me.value?.username ?? '?').slice(0, 2).toUpperCase());
const left = computed(() => sessionLeft(expiryMs(me.value?.expiresAt), now.value));
const expires = computed(() => {
    const t = expiryMs(me.value?.expiresAt);
    return t ? new Date(t).toLocaleString() : '';
});

function toggle() {
    open.value = !open.value;
    now.value = Date.now();
    clearInterval(timer);
    if (open.value) timer = setInterval(() => (now.value = Date.now()), 30000);
}
onBeforeUnmount(() => clearInterval(timer));

const busy = ref(false);
async function signOut() {
    busy.value = true;
    await logout(fetch.bind(globalThis), url => location.assign(url));
}
</script>

<template>
    <div v-if="me" class="acct" @keydown.esc.stop="open && (open = false)">
        <button
            class="btn ghost avatar-btn"
            type="button"
            aria-haspopup="dialog"
            :aria-expanded="open"
            aria-label="Account"
            data-testid="account"
            @click="toggle"
        >
            <span class="avatar" aria-hidden="true">{{ initials }}</span>
        </button>
        <template v-if="open">
            <div class="back" @mousedown="open = false"></div>
            <div class="floating menu" role="dialog" aria-label="Account">
                <div class="who">
                    <b data-testid="account-user">{{ me.username }}</b>
                    <span v-if="me.admin" class="tag">admin</span>
                </div>
                <p v-if="me.groups?.length" class="muted small groups">{{ me.groups.join(', ') }}</p>
                <p v-if="expires" class="muted small" data-testid="account-expiry">Session ends {{ left }} ({{ expires }})</p>
                <p v-if="me.clusterAuth === 'required'" class="small warn">Cluster sign-in required.</p>
                <p class="muted small" data-testid="account-version">{{ versionTitle(agentInfo?.version) }}</p>
                <button class="btn sm" type="button" :disabled="busy" data-testid="logout" @click="signOut">Log out</button>
            </div>
        </template>
    </div>
</template>

<style scoped>
.acct {
    position: relative;
}
.avatar-btn {
    padding: 0 6px;
}
.avatar {
    width: 24px;
    height: 24px;
    border-radius: 12px;
    background: var(--s3);
    display: grid;
    place-items: center;
    font-size: 11px;
    font-weight: 600;
    color: var(--text2);
}
.back {
    position: fixed;
    inset: 0;
    z-index: 30;
}
.menu {
    position: absolute;
    z-index: 31;
    right: 0;
    top: 36px;
    width: 280px;
    padding: 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    align-items: flex-start;
}
.who {
    display: flex;
    gap: 8px;
    align-items: center;
}
.small {
    font-size: 12px;
    margin: 0;
}
.groups {
    overflow-wrap: anywhere;
}
.warn {
    color: var(--warning);
}
</style>
