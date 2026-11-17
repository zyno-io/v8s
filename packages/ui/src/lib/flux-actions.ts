import type { MutationResult, ObjectRef } from '@v8s/client';

import { useUiStore } from '@/stores/ui.ts';

import type { ActionDef } from './actions.ts';

import { newRequestId } from './actions.ts';
import { fluxKindOf, objectKey, trackReconcile } from './flux.ts';
import { callMutating, errorText, isCancelled } from './mutations.ts';

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

const refLabel = (r: ObjectRef) => `${r.namespace ? r.namespace + '/' : ''}${r.name}`;

async function pool<T>(items: readonly T[], limit: number, fn: (item: T) => Promise<void>): Promise<{ item: T; error: unknown }[]> {
    const failures: { item: T; error: unknown }[] = [];
    let next = 0;
    const worker = async () => {
        while (next < items.length) {
            const item = items[next++];
            try {
                await fn(item);
            } catch (error) {
                failures.push({ item, error });
            }
        }
    };
    await Promise.all(Array.from({ length: Math.min(4, limit, items.length) }, worker));
    return failures;
}

/** `object.action` over refs (4 at a time); tracks the returned token for reconcile-style actions. Resolves to overall success. */
export async function runFluxAction(refs: readonly ObjectRef[], actionId: string): Promise<boolean> {
    const ui = useUiStore();
    const contexts = new Set<string>();
    const tracked = TRACKED_ACTIONS.has(actionId);
    const failures = await pool(refs, 4, async ref => {
        const res = await callMutating<MutationResult>(ref.cluster, 'object.action', { requestId: newRequestId(), ref, action: actionId });
        contexts.add(res.usedContext);
        if (tracked) trackReconcile(objectKey({ ...ref, namespace: ref.namespace ?? '' }), res.token);
    });
    const verb = DONE[actionId] ?? actionId;
    const ok = refs.length - failures.length;
    if (ok) {
        const ctx = [...contexts].filter(Boolean);
        const via = ctx.length ? ` as ${ctx.join(', ')}` : '';
        ui.toast('success', refs.length === 1 ? `${verb} ${refLabel(refs[0])}${via}` : `${verb} ${ok} of ${refs.length} objects${via}`);
    }
    const shown = failures.filter(f => !isCancelled(f.error));
    const texts = new Map<string, number>();
    for (const f of shown) {
        const t = errorText(f.error);
        texts.set(t, (texts.get(t) ?? 0) + 1);
    }
    for (const [t, n] of texts)
        ui.toast('danger', shown.length === 1 ? `${refLabel(shown[0].item)}: ${t}` : n > 1 ? `${t} (${n} objects)` : `${t} (${n})`);
    return failures.length === 0;
}
