import type { Agent, Kind, ViewHandle } from '@v8s/client';

import { connectAgent, ProblemsGVR } from '@v8s/client';
import { shallowRef } from 'vue';

import { resolveEndpoint } from './bootstrap.ts';
import { hostedMe, loginUrl, mayRedirectToLogin, probeHosted, sessionEnded } from './hosted.ts';

/** Sends the browser to the hosted login, unless it just came back from one without a session (then the boot shows why). */
function toLogin(path: string): boolean {
    let storage: Storage | null = null;
    try {
        storage = sessionStorage;
    } catch {
        // no storage
    }
    if (!mayRedirectToLogin(storage, Date.now())) {
        bootState.value = 'login-loop';
        return false;
    }
    location.assign(loginUrl(path));
    return true;
}
import { visibleKinds } from './kinds.ts';
import { watchUpdates } from './update.ts';

export type BootState = 'booting' | 'no-endpoint' | 'error' | 'ready' | 'signed-out' | 'login-loop';

export const bootState = shallowRef<BootState>('booting');
export const bootMessage = shallowRef('');
export const kinds = shallowRef<Kind[]>([]);

let agent: Agent | null = null;
let podsAll: ViewHandle | null = null;
let namespacesView: ViewHandle | null = null;
let problemsAll: ViewHandle | null = null;

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

/**
 * Unacknowledged problems across connected clusters (sidebar and top-bar badges, Overview). A one-row live view; rows are never
 * rendered. The problems engine runs in the agent whether or not anyone looks.
 */
export function getProblemsAll(): ViewHandle {
    if (!problemsAll) throw new Error('agent not connected');
    return problemsAll;
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
    // After a hosted logout the page waits for an explicit "Sign in" (the IdP's SSO session would sign the user straight back in).
    if (new URLSearchParams(location.search).has('signedout') && !window.v8sDesktop) {
        bootState.value = 'signed-out';
        return;
    }
    let ep = await resolveEndpoint();
    let hosted = false;
    if (!ep) {
        // Not desktop, not a fragment endpoint: maybe a hosted server, which authenticates by session cookie.
        const probe = await probeHosted(fetch.bind(globalThis), location);
        if (probe.kind === 'login') {
            bootMessage.value = 'Redirecting to sign in…';
            toLogin(location.pathname + location.search);
            return;
        }
        if (probe.kind === 'none') {
            bootState.value = 'no-endpoint';
            return;
        }
        hostedMe.value = probe.me;
        ep = probe.endpoint;
        hosted = true;
    }
    try {
        agent = await connectAgent(ep);
        podsAll = agent.subscribeView({ clusters: [], gvr: 'v1/pods', namespaces: [], text: '', filters: [], sort: [] }, { offset: 0, limit: 1 });
        namespacesView = agent.subscribeView(
            { clusters: [], gvr: 'v1/namespaces', namespaces: [], text: '', filters: [], sort: [{ col: 'name', desc: false }] },
            { offset: 0, limit: 500 }
        );
        problemsAll = agent.subscribeView(
            { clusters: [], gvr: ProblemsGVR, namespaces: [], text: '', filters: [{ col: 'acked', op: 'eq', value: 'false' }], sort: [] },
            { offset: 0, limit: 1 }
        );
        bootState.value = 'ready';
        watchUpdates(agent);
        refreshKinds(0);
    } catch (err) {
        // A hosted socket that fails to open usually means the session ended (the upgrade answers 401, which the browser hides).
        if (hosted && (await sessionEnded(fetch.bind(globalThis)))) {
            toLogin(location.pathname + location.search);
            return;
        }
        bootMessage.value = err instanceof Error ? err.message : String(err);
        bootState.value = 'error';
    }
}
