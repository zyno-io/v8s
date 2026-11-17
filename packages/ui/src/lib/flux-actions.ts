import type { MutationResult, ObjectRef } from '@v8s/client';

import type { ActionDef } from './actions.ts';

import { newRequestId } from './actions.ts';
import { fluxKindOf, objectKey, trackReconcile } from './flux.ts';
import { bulkDecisions, callMutating, pool, report } from './mutations.ts';

const FLUX = {
    suspend: { id: 'flux.suspend', label: 'Suspend', verb: 'patch', param: null, bulk: true },
    resume: { id: 'flux.resume', label: 'Resume', verb: 'patch', param: null, bulk: true },
    reconcile: { id: 'flux.reconcile', label: 'Reconcile', verb: 'patch', param: null, bulk: true },
    reconcileWithSource: { id: 'flux.reconcileWithSource', label: 'Reconcile with source', verb: 'patch', param: null, bulk: true },
    force: { id: 'flux.force', label: 'Force reconcile', verb: 'patch', param: null, bulk: true },
    reset: { id: 'flux.reset', label: 'Reset failures', verb: 'patch', param: null, bulk: true }
} satisfies Record<string, ActionDef>;

/** Actions for a Flux kind (empty for others). Sources have no "reconcile with source"; force and reset are HelmRelease-only. */
export function fluxActionDefs(gvr: string): ActionDef[] {
    const k = fluxKindOf(gvr);
    if (!k) return [];
    const out: ActionDef[] = [FLUX.suspend, FLUX.resume, FLUX.reconcile];
    if (!k.source) out.push(FLUX.reconcileWithSource);
    if (k.id === 'helmrelease') out.push(FLUX.force, FLUX.reset);
    return out;
}

/** Actions after which the object is `Reconciling…` until `lastHandledReconcileAt` reaches the returned token. */
export const TRACKED_ACTIONS: ReadonlySet<string> = new Set(['flux.reconcile', 'flux.reconcileWithSource', 'flux.force', 'flux.reset']);

const DONE: Record<string, string> = {
    'flux.suspend': 'Suspended',
    'flux.resume': 'Resumed',
    'flux.reconcile': 'Reconcile requested for',
    'flux.reconcileWithSource': 'Reconcile with source requested for',
    'flux.force': 'Force reconcile requested for',
    'flux.reset': 'Reset failures requested for'
};

/** `object.action` over refs (4 at a time); tracks the returned token for reconcile-style actions. Resolves to overall success. */
export async function runFluxAction(refs: readonly ObjectRef[], actionId: string): Promise<boolean> {
    const contexts = new Set<string>();
    const decisions = bulkDecisions();
    const tracked = TRACKED_ACTIONS.has(actionId);
    const failures = await pool(refs, 4, async ref => {
        const res = await callMutating<MutationResult>(
            ref.cluster,
            'object.action',
            { requestId: newRequestId(), ref, action: actionId },
            { decision: decisions(ref.cluster) }
        );
        contexts.add(res.usedContext);
        if (tracked) trackReconcile(objectKey({ ...ref, namespace: ref.namespace ?? '' }), res.token);
    });
    report(DONE[actionId] ?? actionId, refs, failures, contexts);
    return failures.length === 0;
}
