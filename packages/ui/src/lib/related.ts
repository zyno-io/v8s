import type { Filter } from '@v8s/client';

/**
 * Workload kinds whose pods the Related tab lists. A pod's `owner` column names its controlling owner: the workload itself, or for
 * a Deployment one of its ReplicaSets (so the Deployment's pods are those of the ReplicaSets it owns).
 */
const POD_OWNERS: Record<string, 'self' | 'replicasets'> = {
    'apps/v1/deployments': 'replicasets',
    'apps/v1/statefulsets': 'self',
    'apps/v1/daemonsets': 'self',
    'apps/v1/replicasets': 'self',
    'batch/v1/jobs': 'self'
};

/** Whether the Related tab of this kind lists its pods (the ready count then opens that tab). */
export const listsPods = (gvr: string): boolean => gvr in POD_OWNERS;

/** Whether the pods are found through the workload's ReplicaSets (Deployments). */
export const podsViaReplicaSets = (gvr: string): boolean => POD_OWNERS[gvr] === 'replicasets';

/**
 * The pods view filter for a workload: pods owned by `name`, or by any of `replicaSets` for a Deployment.
 * Null when there is nothing to match (not a pod-owning kind, or a Deployment with no ReplicaSets yet).
 */
export function podsFilter(gvr: string, name: string, replicaSets: readonly string[] = []): Filter | null {
    const how = POD_OWNERS[gvr];
    if (!how || !name) return null;
    if (how === 'self') return { col: 'owner', op: 'eq', value: name };
    return replicaSets.length ? { col: 'owner', op: 'in', value: [...replicaSets].sort() } : null;
}
