<script setup lang="ts">
import { computed, ref } from 'vue';

import { getAgent } from '@/lib/agent.ts';
import { dismissedUpdate, dismissPatch, pendingUpdate, updateStatus } from '@/lib/update.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useUiStore } from '@/stores/ui.ts';

const settings = useSettingsStore();
const ui = useUiStore();
const info = getAgent().agentInfo;

const open = ref(false);
const hosted = computed(() => info.value?.mode === 'hosted');
// Wait for settings: the dismissed version lives there, and showing the pill just to hide it would flash.
const pending = computed(() => (settings.loaded ? pendingUpdate(updateStatus.value, dismissedUpdate(settings.doc.uiPrefs)) : null));
const label = computed(() => (hosted.value ? 'Server update' : 'Update'));
const current = computed(() => (info.value?.version ?? '').replace(/^v/, ''));
// The agent builds this URL; still only ever link out over https.
const href = computed(() => (pending.value?.url?.startsWith('https://') ? pending.value.url : ''));

function dismiss() {
    const v = pending.value?.latest;
    if (!v) return;
    open.value = false;
    settings.patch(dismissPatch(v)).catch(e => ui.toast('danger', e instanceof Error ? e.message : String(e)));
}
</script>

<template>
    <div v-if="pending" class="upd" @keydown.esc.stop="open && (open = false)">
        <button
            class="tag release pill"
            type="button"
            aria-haspopup="dialog"
            :aria-expanded="open"
            :aria-label="`${label}: v8s ${pending.latest} is available`"
            data-testid="update-indicator"
            @click="open = !open"
        >
            <svg width="11" height="11" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
                <path d="M8 13V3M3.5 7.5L8 3l4.5 4.5" />
            </svg>
            <span class="plabel">{{ label }}</span>
        </button>
        <template v-if="open">
            <div class="back" @mousedown="open = false"></div>
            <div class="floating menu" role="dialog" aria-label="Update available" data-testid="update-popover">
                <p v-if="hosted">v8s <b>{{ pending.latest }}</b> is available. This server runs {{ current }}; your administrator updates it.</p>
                <p v-else>v8s <b>{{ pending.latest }}</b> is available. You have {{ current }}.</p>
                <div class="row">
                    <a v-if="href" :href="href" target="_blank" rel="noopener noreferrer" data-testid="update-link">{{
                        hosted ? 'Release notes ↗' : 'Release notes and downloads ↗'
                    }}</a>
                    <button class="btn sm" type="button" data-testid="update-dismiss" @click="dismiss">Dismiss</button>
                </div>
            </div>
        </template>
    </div>
</template>

<style scoped>
.upd {
    position: relative;
}
.pill {
    height: 24px;
    padding: 0 8px;
    font-family: inherit;
    cursor: pointer;
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
    width: 310px;
    padding: 12px;
    display: flex;
    flex-direction: column;
    gap: 10px;
}
.menu p {
    margin: 0;
    font-size: 13px;
}
.row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    font-size: 12px;
}
.row a {
    white-space: nowrap;
}
@media (max-width: 899.98px) {
    .plabel {
        display: none;
    }
}
@media (max-width: 639.98px) {
    .upd {
        display: none;
    }
}
</style>
