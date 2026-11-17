import type { HubbleStatus } from '@v8s/client';

import { defineStore } from 'pinia';
import { computed, shallowRef } from 'vue';

import { getAgent, kinds } from '@/lib/agent.ts';
import { detectIntegrations } from '@/lib/integrations.ts';
import { useClustersStore } from '@/stores/clusters.ts';

/** Which optional integrations the selected clusters have. Kyverno / Trivy / Kubescape come from discovery; Hubble is probed per cluster. */
export const useIntegrationsStore = defineStore('integrations', () => {
    const clusters = useClustersStore();
    const detected = computed(() => detectIntegrations(kinds.value));
    /** Clusters whose hubble-relay answered `hubble.status` with `available`. */
    const hubbleClusters = shallowRef<string[]>([]);
    const hubbleStatus = shallowRef<Record<string, HubbleStatus>>({});
    const hubble = computed(() => hubbleClusters.value.length > 0);
    let seq = 0;
    let probed = '';

    /** Probes `hubble.status` of every ready cluster (once per set of ready clusters; call `refreshHubble(true)` to force). */
    async function refreshHubble(force = false): Promise<void> {
        const ready = clusters.states.filter(s => s.phase === 'ready' || s.phase === 'degraded').map(s => s.id);
        const key = ready.join('\n');
        if (!force && key === probed) return;
        probed = key;
        const mine = ++seq;
        const entries = await Promise.all(
            ready.map(async id => {
                try {
                    return [id, await getAgent().call<HubbleStatus>('hubble.status', { cluster: id })] as const;
                } catch {
                    return null;
                }
            })
        );
        if (mine !== seq) return;
        const status: Record<string, HubbleStatus> = {};
        for (const e of entries) if (e) status[e[0]] = e[1];
        hubbleStatus.value = status;
        hubbleClusters.value = Object.keys(status).filter(id => status[id].available);
    }

    return { detected, hubble, hubbleClusters, hubbleStatus, refreshHubble };
});
