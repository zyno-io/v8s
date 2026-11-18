<script setup lang="ts">
import LockIcon from '@/components/LockIcon.vue';
import { useClustersStore } from '@/stores/clusters.ts';

/** A small lock beside a cluster name when that cluster is protected; renders nothing otherwise. */
defineProps<{ cluster: string }>();
const clusters = useClustersStore();
</script>

<template>
    <span v-if="clusters.isProtected(cluster)" class="pmark" role="img" aria-label="Protected" title="Protected"><LockIcon :size="12" /></span>
</template>

<style scoped>
.pmark {
    display: inline-flex;
    align-items: center;
    flex: none;
    color: var(--danger);
}
</style>
