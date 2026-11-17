import type { Agent, Kind, ViewHandle } from '@v8s/client';

import { connectAgent } from '@v8s/client';
import { shallowRef } from 'vue';

import { resolveEndpoint } from './bootstrap.ts';
import { visibleKinds } from './kinds.ts';

export type BootState = 'booting' | 'no-endpoint' | 'error' | 'ready';

export const bootState = shallowRef<BootState>('booting');
export const bootMessage = shallowRef('');
export const kinds = shallowRef<Kind[]>([]);

let agent: Agent | null = null;
let podsAll: ViewHandle | null = null;
let namespacesView: ViewHandle | null = null;

export function getAgent(): Agent {
    if (!agent) throw new Error('agent not connected');
    return agent;
}

/** Unfiltered pod count across connected clusters (sidebar, page header). A one-row live view, no rows rendered. */
export function getPodsAll(): ViewHandle {
    if (!podsAll) throw new Error('agent not connected');
    return podsAll;
}

/**
 * The `v1/namespaces` view, held for the app's lifetime (tier-0 data: a few dozen rows per cluster). Namespace filters,
 * the command bar and the create dialog read names from it instead of scanning another view's rows.
 */
export function getNamespacesView(): ViewHandle {
    if (!namespacesView) throw new Error('agent not connected');
    return namespacesView;
}

let kindsTimer: ReturnType<typeof setTimeout> | undefined;
/** Re-reads `kinds.list` (debounced): selection changes and a cluster finishing discovery both change the union. */
export function refreshKinds(delay = 250): void {
    clearTimeout(kindsTimer);
    kindsTimer = setTimeout(async () => {
        try {
            const next = visibleKinds((await agent?.listKinds())?.kinds ?? []);
            if (kindsSignature(next) !== kindsSignature(kinds.value)) kinds.value = next;
        } catch {
            // keep the previous list
        }
    }, delay);
}

function kindsSignature(list: readonly Kind[]): string {
    let h = list.length + ':';
    for (const k of list) h += k.gvr + (k.available ? '+' : '-') + k.clusters.length + ',';
    return h;
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
        namespacesView = agent.subscribeView(
            { clusters: [], gvr: 'v1/namespaces', namespaces: [], text: '', filters: [], sort: [{ col: 'name', desc: false }] },
            { offset: 0, limit: 500 }
        );
        bootState.value = 'ready';
        refreshKinds(0);
    } catch (err) {
        bootMessage.value = err instanceof Error ? err.message : String(err);
        bootState.value = 'error';
    }
}
