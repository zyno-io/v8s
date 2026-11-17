<script setup lang="ts">
import { computed } from 'vue';

import { getPodsAll } from '@/lib/agent.ts';
import { formatCount } from '@/lib/format.ts';

const podsAll = getPodsAll();
const podCount = computed(() => (podsAll.version.value >= 0 && podsAll.total.value ? formatCount(podsAll.total.value) : ''));

// Static for P0 except Pods and Problems.
const sections: { title: string; items: string[] }[] = [
    { title: 'Cluster', items: ['Overview', 'Nodes', 'Events'] },
    { title: 'Workloads', items: ['Deployments', 'StatefulSets', 'DaemonSets', 'Jobs', 'CronJobs'] },
    { title: 'Network · Config · Storage', items: ['Services', 'Ingresses', 'ConfigMaps & Secrets', 'Volumes'] },
    { title: 'Integrations', items: ['Flux', 'Helm', 'Security', 'Custom resources'] }
];
</script>

<template>
    <nav class="side" aria-label="Resources">
        <div class="navsec">Starred</div>
        <router-link class="nav" to="/pods">Pods<span class="n">{{ podCount }}</span></router-link>
        <template v-for="s in sections" :key="s.title">
            <div class="navsec">{{ s.title }}</div>
            <router-link v-if="s.title === 'Cluster'" class="nav" to="/problems">Problems</router-link>
            <a v-for="it in s.items" :key="it" class="nav" href="#" aria-disabled="true" @click.prevent>{{ it }}</a>
        </template>
    </nav>
</template>
