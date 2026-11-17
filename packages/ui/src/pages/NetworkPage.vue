<script setup lang="ts">
import { computed, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import DropsView from '@/components/DropsView.vue';
import HubbleStatus from '@/components/HubbleStatus.vue';
import ServiceMapView from '@/components/ServiceMapView.vue';
import { isPhone } from '@/lib/viewport.ts';

const route = useRoute();
const router = useRouter();

const view = computed(() => (route.name === 'network-map' ? 'map' : route.name === 'network-status' ? 'status' : 'drops'));

// Phones get the drops view and the status panel, not the canvas map.
watch(
    [view, isPhone],
    ([v, phone]) => {
        if (v === 'map' && phone) void router.replace({ name: 'network-drops' });
    },
    { immediate: true }
);
</script>

<template>
    <main class="netpage" data-testid="network-page">
        <ServiceMapView v-if="view === 'map' && !isPhone" />
        <HubbleStatus v-else-if="view === 'status'" />
        <DropsView v-else-if="view === 'drops'" />
    </main>
</template>

<style scoped>
.netpage {
    flex-grow: 1;
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
}
</style>
