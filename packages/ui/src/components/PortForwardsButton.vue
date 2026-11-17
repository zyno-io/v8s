<script setup lang="ts">
import { defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue';

import { listForwards, portForwardAvailable, portForwardsOpen } from '@/lib/portforward.ts';

const PortForwardsPanel = defineAsyncComponent(() => import('@/components/PortForwardsPanel.vue'));

const available = portForwardAvailable();
const count = ref(0);
let timer: ReturnType<typeof setInterval> | undefined;

async function refresh() {
    if (document.hidden) return;
    try {
        count.value = (await listForwards()).filter(f => f.state !== 'stopped').length;
    } catch {
        // Count is best-effort.
    }
}
function onVis() {
    if (!document.hidden) void refresh();
}

watch(portForwardsOpen, open => {
    if (!open) void refresh();
});

onMounted(() => {
    if (!available) return;
    void refresh();
    timer = setInterval(refresh, 15000);
    document.addEventListener('visibilitychange', onVis);
});
onBeforeUnmount(() => {
    clearInterval(timer);
    document.removeEventListener('visibilitychange', onVis);
});
</script>

<template>
    <template v-if="available">
        <button
            class="btn sm"
            type="button"
            aria-haspopup="dialog"
            :aria-expanded="portForwardsOpen"
            :aria-label="`Port forwards, ${count} active`"
            data-testid="pf-button"
            @click="portForwardsOpen = !portForwardsOpen"
        >
            Forwards <span class="tag" :class="count ? 'success' : ''">{{ count }}</span>
        </button>
        <PortForwardsPanel v-if="portForwardsOpen" />
    </template>
</template>
