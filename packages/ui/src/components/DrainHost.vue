<script setup lang="ts">
import DrainDialog from '@/components/DrainDialog.vue';
import { drainRequest } from '@/lib/jobs.ts';
</script>

<template>
    <DrainDialog
        v-if="drainRequest"
        :key="`${drainRequest.cluster}/${drainRequest.node}/${drainRequest.jobId ?? ''}`"
        :cluster="drainRequest.cluster"
        :node="drainRequest.node"
        :job-id="drainRequest.jobId"
        @close="drainRequest = null"
    />
</template>
