import type { AccessCheck, AccessResult, ObjectRef } from '@v8s/client';

export type ActionParam = 'scale' | 'rollback' | null;

export interface ActionDef {
    id: string;
    label: string;
    /** Kubernetes verb used for the access check. */
    verb: string;
    /** The resource the check is made on when it differs from the object's own (trigger creates a Job). */
    checkGvr?: string;
    danger?: boolean;
    param: ActionParam;
    /** Offered on the bulk bar for a multi-selection. */
    bulk: boolean;
    /** Handled in the UI (a dialog, a dock) rather than by an `object.action` request. */
    client?: 'drain' | 'nodeshell' | 'portforward';
    /** No access check (the verb is on a subresource `access.check` cannot name). */
    noCheck?: boolean;
}

const DEPLOYMENTS = 'apps/v1/deployments';
const STATEFULSETS = 'apps/v1/statefulsets';
const DAEMONSETS = 'apps/v1/daemonsets';
const REPLICASETS = 'apps/v1/replicasets';
const NODES = 'v1/nodes';
const CRONJOBS = 'batch/v1/cronjobs';

const A = {
    scale: { id: 'scale', label: 'Scale…', verb: 'patch', param: 'scale', bulk: false },
    restart: { id: 'restart', label: 'Restart', verb: 'patch', param: null, bulk: true },
    cordon: { id: 'cordon', label: 'Cordon', verb: 'patch', param: null, bulk: true },
    uncordon: { id: 'uncordon', label: 'Uncordon', verb: 'patch', param: null, bulk: true },
    suspend: { id: 'suspend', label: 'Suspend', verb: 'patch', param: null, bulk: true },
    resume: { id: 'resume', label: 'Resume', verb: 'patch', param: null, bulk: true },
    trigger: { id: 'trigger', label: 'Trigger now', verb: 'create', checkGvr: 'batch/v1/jobs', param: null, bulk: false },
    rollback: { id: 'rollback', label: 'Roll back…', verb: 'patch', param: 'rollback', bulk: false },
    drain: { id: 'drain', label: 'Drain…', verb: 'patch', param: null, bulk: false, client: 'drain' },
    nodeshell: { id: 'nodeshell', label: 'Shell…', verb: 'create', checkGvr: 'v1/pods', param: null, bulk: false, client: 'nodeshell' },
    portforward: { id: 'portforward', label: 'Port forward…', verb: 'create', param: null, bulk: false, client: 'portforward', noCheck: true },
    delete: { id: 'delete', label: 'Delete', verb: 'delete', danger: true, param: null, bulk: true }
} satisfies Record<string, ActionDef>;

const BY_KIND: Record<string, ActionDef[]> = {
    [DEPLOYMENTS]: [A.scale, A.restart, A.rollback],
    [STATEFULSETS]: [A.scale, A.restart],
    [DAEMONSETS]: [A.restart],
    [REPLICASETS]: [A.scale],
    [NODES]: [A.cordon, A.uncordon, A.drain, A.nodeshell],
    'v1/pods': [A.portforward],
    'v1/services': [A.portforward],
    [CRONJOBS]: [A.suspend, A.resume, A.trigger]
};

/** Actions for a kind: its specific ones, then Delete (every kind). */
export function actionsFor(gvr: string): ActionDef[] {
    return [...(BY_KIND[gvr] ?? []), A.delete];
}

export function actionById(gvr: string, id: string): ActionDef | undefined {
    return actionsFor(gvr).find(a => a.id === id);
}

export function accessCheckFor(action: ActionDef, ref: ObjectRef): AccessCheck {
    const isOwn = !action.checkGvr;
    return {
        cluster: ref.cluster,
        verb: action.verb,
        gvr: action.checkGvr ?? ref.gvr,
        namespace: ref.namespace || undefined,
        name: isOwn ? ref.name : undefined
    };
}

export interface ActionState {
    disabled: boolean;
    /** Tooltip: why it is disabled, or the write-context hint. */
    reason: string;
    viaWrite: boolean;
}

/** Combines per-target access results: all must be allowed; any via-write marks the hint. Unknown (no result yet) stays enabled. */
export function actionState(results: readonly (AccessResult | undefined)[]): ActionState {
    let viaWrite = false;
    for (const r of results) {
        if (!r) continue;
        if (!r.allowed) return { disabled: true, reason: r.reason || 'Not permitted', viaWrite: false };
        if (r.viaWrite) viaWrite = true;
    }
    return { disabled: false, reason: viaWrite ? 'Will run as the linked write context' : '', viaWrite };
}

export interface ConfirmInput {
    count: number;
    /** Distinct clusters of the targets. */
    clusters: readonly string[];
    /** The targets' clusters that are protected. */
    protectedClusters: readonly string[];
    /** Destructive action (delete) on a phone: never a single tap. */
    destructive?: boolean;
    phone?: boolean;
}

/** Single object: plain confirm. Bulk, cross-cluster, protected-cluster, or a destructive action on a phone: type-to-confirm. */
export function needsTypeToConfirm(i: ConfirmInput): boolean {
    if (i.count > 1 || i.clusters.length > 1) return true;
    if (i.destructive && i.phone) return true;
    return i.clusters.some(c => i.protectedClusters.includes(c));
}

/** What the user must type: the object name for one, the count for several. */
export function confirmPhrase(count: number, name: string): string {
    return count === 1 ? name : String(count);
}

/** Bulk or cross-cluster: always type-to-confirm, whatever the action. */
export function isBulkOrCross(count: number, clusterCount: number): boolean {
    return count > 1 || clusterCount > 1;
}

/** What a confirm names: the object (or the count) and the cluster names, spelled out. */
export function targetSummary(targets: readonly { namespace?: string; name?: string }[], clusterNames: readonly string[]): string {
    const where =
        clusterNames.length > 1 ? ` across ${clusterNames.length} clusters (${clusterNames.join(', ')})` : ` on ${clusterNames[0] ?? 'the cluster'}`;
    if (targets.length === 1) {
        const t = targets[0];
        return `${t.namespace ? t.namespace + '/' : ''}${t.name ?? ''}${where}`;
    }
    return `${targets.length} objects${where}`;
}

export function newRequestId(): string {
    const c = globalThis.crypto;
    if (c && typeof c.randomUUID === 'function') return c.randomUUID();
    const b = new Uint8Array(16);
    (c ?? { getRandomValues: (a: Uint8Array) => a.map(() => Math.floor(Math.random() * 256)) }).getRandomValues(b);
    b[6] = (b[6] & 0x0f) | 0x40;
    b[8] = (b[8] & 0x3f) | 0x80;
    const h = [...b].map(x => x.toString(16).padStart(2, '0')).join('');
    return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}
