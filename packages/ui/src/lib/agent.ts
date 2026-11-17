import type { Agent, Kind, ViewHandle } from '@v8s/client';

import { connectAgent } from '@v8s/client';
import { shallowRef } from 'vue';

import { resolveEndpoint } from './bootstrap.ts';

export type BootState = 'booting' | 'no-endpoint' | 'error' | 'ready';

export const bootState = shallowRef<BootState>('booting');
export const bootMessage = shallowRef('');
export const kinds = shallowRef<Kind[]>([]);

let agent: Agent | null = null;
let podsAll: ViewHandle | null = null;

export function getAgent(): Agent {
    if (!agent) throw new Error('agent not connected');
    return agent;
}

/** Unfiltered pod count across connected clusters (sidebar, page header). A one-row live view, no rows rendered. */
export function getPodsAll(): ViewHandle {
    if (!podsAll) throw new Error('agent not connected');
    return podsAll;
}

export async function startAgent(): Promise<void> {
    bootState.value = 'booting';
    const ep = await resolveEndpoint();
    if (!ep) {
        bootState.value = 'no-endpoint';
        return;
    }
    try {
        agent = await connectAgent(ep);
        podsAll = agent.subscribeView({ clusters: [], gvr: 'v1/pods', namespaces: [], text: '', filters: [], sort: [] }, { offset: 0, limit: 1 });
        bootState.value = 'ready';
        agent
            .listKinds()
            .then(r => (kinds.value = r.kinds))
            .catch(() => {});
    } catch (err) {
        bootMessage.value = err instanceof Error ? err.message : String(err);
        bootState.value = 'error';
    }
}
