import type { Filter, Kind, ViewQuery } from '@v8s/client';

import { ProblemsGVR } from '@v8s/client';

import type { Integrations } from './integrations.ts';

import { detectIntegrations } from './integrations.ts';

/** Severities in the order the Overview lists them. */
export const SEVERITIES = ['critical', 'error', 'warning', 'info'] as const;
export type Severity = (typeof SEVERITIES)[number];

export const NODES_GVR = 'v1/nodes';
export const PODS_GVR = 'v1/pods';
export const KUSTOMIZATIONS_SUFFIX = '/kustomizations';
export const FLUX_HELMRELEASES_SUFFIX = '/helmreleases';
export const HELM_GVR = 'helm.sh/v1/releases';

/** A one-row count view of `gvr` in one cluster, optionally narrowed by tones (statusTone codes) and extra filters. */
export function countQuery(cluster: string, gvr: string, tones: readonly number[] = [], extra: Filter[] = []): ViewQuery {
    const filters: Filter[] = [...extra];
    if (tones.length) filters.push({ col: 'statusTone', op: 'in', value: tones.map(String) });
    return { clusters: [cluster], gvr, namespaces: [], text: '', filters, sort: [] };
}

/** Unacknowledged problems of one severity in one cluster. */
export function problemsQuery(cluster: string, severity: Severity): ViewQuery {
    return {
        clusters: [cluster],
        gvr: ProblemsGVR,
        namespaces: [],
        text: '',
        filters: [
            { col: 'acked', op: 'eq', value: 'false' },
            { col: 'severity', op: 'in', value: [severity] }
        ],
        sort: []
    };
}

export interface HealthKinds {
    kustomizations: string;
    helmReleases: string;
    helmSecrets: string;
}

/** The Flux / Helm gvrs this cluster serves (empty string when absent). */
export function healthKinds(kinds: readonly Pick<Kind, 'gvr' | 'clusters'>[], cluster: string): HealthKinds {
    const served = kinds.filter(k => k.clusters.includes(cluster));
    return {
        kustomizations: served.find(k => /^kustomize\.toolkit\.fluxcd\.io\/[^/]+\/kustomizations$/.test(k.gvr))?.gvr ?? '',
        helmReleases: served.find(k => /^helm\.toolkit\.fluxcd\.io\/[^/]+\/helmreleases$/.test(k.gvr))?.gvr ?? '',
        helmSecrets: served.find(k => k.gvr === HELM_GVR)?.gvr ?? ''
    };
}

/** Integrations installed in one cluster, from the union kinds list (`Kind.clusters` says which clusters serve each). */
export function integrationsOf(kinds: readonly Pick<Kind, 'group' | 'clusters'>[], cluster: string): Integrations {
    return detectIntegrations(kinds.filter(k => k.clusters.includes(cluster)));
}

export interface PodCounts {
    total: number;
    running: number;
    failing: number;
    pending: number;
    completed: number;
}

/** Tone buckets must add up to the total: anything the counted tones miss is "other". */
export function podOther(c: PodCounts): number {
    return Math.max(0, c.total - c.running - c.failing - c.pending - c.completed);
}

export const phaseLabel = (phase: string): string =>
    ({
        ready: 'Connected',
        syncing: 'Syncing',
        connecting: 'Connecting',
        degraded: 'Degraded',
        reconnecting: 'Reconnecting',
        unreachable: 'Unreachable',
        'auth-required': 'Sign-in required'
    })[phase] ?? phase;
