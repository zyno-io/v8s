<script setup lang="ts">
import { computed } from 'vue';

import ClusterOverviewCard from '@/components/ClusterOverviewCard.vue';
import { useClustersStore } from '@/stores/clusters.ts';

const clusters = useClustersStore();
const ids = computed(() => clusters.activeIds);
</script>

<template>
    <main class="page" data-testid="overview">
        <div class="head">
            <h1>Overview</h1>
            <span class="muted">{{ ids.length }} cluster{{ ids.length === 1 ? '' : 's' }}</span>
        </div>
        <div v-if="!ids.length" class="empty card">
            <p>No clusters connected.</p>
            <button class="btn" type="button" @click="clusters.open()">Choose clusters</button>
        </div>
        <div v-else class="cards">
            <ClusterOverviewCard v-for="id in ids" :key="id" :id="id" />
        </div>
    </main>
</template>

<style scoped>
.page {
    flex: 1 1 0;
    min-height: 0;
    overflow: auto;
    padding: 16px 20px;
    display: flex;
    flex-direction: column;
    gap: 12px;
}
.head {
    display: flex;
    align-items: baseline;
    gap: 12px;
}
h1 {
    font-size: 19px;
    font-weight: 650;
    letter-spacing: -0.01em;
}
.cards {
    display: flex;
    flex-direction: column;
    gap: 12px;
}
.empty {
    padding: 36px 16px;
    text-align: center;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
}
@media (max-width: 639.98px) {
    .page {
        padding: 12px;
    }
}
</style>
