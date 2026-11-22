import type { MutationResult, ObjectRef } from '@v8s/client';

import { RpcError } from '@v8s/client';

import { useClustersStore } from '@/stores/clusters.ts';
import { useUiStore } from '@/stores/ui.ts';

import { newRequestId } from './actions.ts';
import { getAgent } from './agent.ts';
import { ask } from './ask.ts';

export const WRITE_UNAVAILABLE_TEXT = 'The write context was rejected — is your elevated session active?';

/** Human text for a failed request. `write_unavailable` leads with the standard hint, then the server's own message. */
export function errorText(e: unknown): string {
    if (e instanceof RpcError) {
        const base =
            e.code === 'write_unavailable' ? (e.message ? `${WRITE_UNAVAILABLE_TEXT} ${e.message}` : WRITE_UNAVAILABLE_TEXT) : e.message || e.code;
        return e.hint ? `${base} ${e.hint}` : base;
    }
    return e instanceof Error ? e.message : String(e);
}

/** True for the error `callMutating` throws when the user declines a confirmation: nothing ran, nothing to report. */
export const isCancelled = (e: unknown): boolean => e instanceof RpcError && e.code === 'cancelled';

/** "Runs as <write context>" or "Runs as your current context", for confirm dialogs. */
export function runAs(cluster: string, viaWrite: boolean): string {
    if (!viaWrite && !useClustersStore().isElevated(cluster)) return 'Runs as your current context';
    const clusters = useClustersStore();
    const ctx = clusters.writeContextOf(cluster);
    return `Runs as ${ctx || 'the linked write context'}${clusters.isElevated(cluster) ? ' (elevated)' : ''}`;
}

/** One dialog per (kind, cluster) at a time: concurrent callers for the same cluster share the answer instead of stacking dialogs. */
const inflight = new Map<string, Promise<boolean>>();
function once(key: string, fn: () => Promise<boolean>): Promise<boolean> {
    let p = inflight.get(key);
    if (!p) {
        p = fn().finally(() => inflight.delete(key));
        inflight.set(key, p);
    }
    return p;
}

/**
 * The user's answer to "run this through the write context?" for one cluster. Pass the same object to every call that belongs
 * together: once accepted, later calls send `useWriteContext: true` up front (a dry run's answer carries into the apply; a bulk
 * run asks once). `sticky` (bulk) also remembers a refusal, so the rest of the run is cancelled without calling the agent.
 */
export interface WriteDecision {
    accepted: boolean;
    declined: boolean;
    sticky: boolean;
    pending?: Promise<boolean>;
}

export const newWriteDecision = (sticky = false): WriteDecision => ({ accepted: false, declined: false, sticky });

/** One sticky decision per cluster, for one bulk run. */
export function bulkDecisions(): (cluster: string) => WriteDecision {
    const m = new Map<string, WriteDecision>();
    return cluster => {
        let d = m.get(cluster);
        if (!d) m.set(cluster, (d = newWriteDecision(true)));
        return d;
    };
}

export interface ConfirmExtra {
    useWriteContext?: true;
    /** True for the retry after a `write_confirm`: the agent cached the refusal under the first requestId. */
    retry: boolean;
}

async function decideWrite(cluster: string, d: WriteDecision | undefined): Promise<boolean> {
    if (d?.accepted) return true;
    if (d?.declined && d.sticky) return false;
    if (d?.pending) return d.pending;
    const clusters = useClustersStore();
    const ctx = clusters.writeContextOf(cluster) || 'the linked write context';
    const name = clusters.nameById.get(cluster) ?? cluster;
    const p = once(`confirm|${cluster}`, () =>
        ask({
            title: `Run as ${ctx}?`,
            body: `Your current context cannot do this on ${name}. Run it through the linked write context ${ctx} instead? Runs as ${ctx}.`,
            confirmLabel: 'Run'
        })
    );
    if (d) d.pending = p;
    const ok = await p;
    if (d) {
        d.pending = undefined;
        if (ok) d.accepted = true;
        else d.declined = true;
    }
    return ok;
}

/**
 * Runs `fn` (any call that accepts `useWriteContext`: a request or `openStream`) with the two context-routing errors handled
 * (docs/protocol.md, Mutations): `write_confirm` asks the user and retries once with `useWriteContext: true`; `link_mismatch`
 * explains that nothing was written and offers the cluster selector. Declining rejects with `cancelled`.
 * A mutation's retry must carry a fresh requestId (`extra.retry`): the agent de-duplicates by requestId, errors included.
 */
export async function withWriteConfirm<T>(cluster: string, fn: (extra: ConfirmExtra) => Promise<T>, decision?: WriteDecision): Promise<T> {
    if (decision?.declined && decision.sticky) throw new RpcError('cancelled', 'Cancelled');
    try {
        return await fn(decision?.accepted ? { useWriteContext: true, retry: false } : { retry: false });
    } catch (e) {
        if (!(e instanceof RpcError)) throw e;
        const clusters = useClustersStore();
        if (e.code === 'write_confirm') {
            if (!(await decideWrite(cluster, decision))) throw new RpcError('cancelled', 'Cancelled');
            return await fn({ useWriteContext: true, retry: true });
        }
        if (e.code === 'link_mismatch') {
            const ctx = clusters.writeContextOf(cluster) || 'The linked write context';
            const open = await once(`link|${cluster}`, () =>
                ask({
                    title: 'Write context reaches a different cluster',
                    body: `${ctx} does not reach the same cluster as the read context (their kube-system namespaces differ), so nothing was written. Check the link in the cluster selector.`,
                    confirmLabel: 'Open cluster selector'
                })
            );
            if (open) clusters.open();
        }
        throw e;
    }
}

export interface MutatingOpts {
    decision?: WriteDecision;
    /** Called with the fresh requestId when the `write_confirm` retry replaces it: keep it for any later retry of the same intent. */
    onRequestId?: (id: string) => void;
}

/** `agent.call` for a mutation through `withWriteConfirm`. The `useWriteContext` retry uses a fresh `requestId` (reported through `onRequestId`). */
export function callMutating<T>(cluster: string, method: string, params: Record<string, unknown>, opts: MutatingOpts = {}): Promise<T> {
    return withWriteConfirm(
        cluster,
        extra => {
            const p: Record<string, unknown> = { ...params };
            if (extra.retry && 'requestId' in params) {
                const id = newRequestId();
                p.requestId = id;
                opts.onRequestId?.(id);
            }
            if (extra.useWriteContext) p.useWriteContext = true;
            return getAgent().call<T>(method, p);
        },
        opts.decision
    );
}

const refLabel = (r: ObjectRef) => `${r.namespace ? r.namespace + '/' : ''}${r.name}`;

/** Runs `fn` over items, `limit` at a time; collects errors per item. */
export async function pool<T>(items: readonly T[], limit: number, fn: (item: T) => Promise<void>): Promise<{ item: T; error: unknown }[]> {
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
    await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
    return failures;
}

export function report(verb: string, refs: readonly ObjectRef[], failures: { item: ObjectRef; error: unknown }[], contexts: Set<string>) {
    const ui = useUiStore();
    const ok = refs.length - failures.length;
    if (ok) {
        const ctx = [...contexts].filter(Boolean);
        const via = ctx.length ? ` as ${ctx.join(', ')}` : '';
        ui.toast('success', refs.length === 1 ? `${verb} ${refLabel(refs[0])}${via}` : `${verb} ${ok} of ${refs.length} objects${via}`);
    }
    // Distinct error texts once each, so a bulk failure is one toast, not fifty.
    const texts = new Map<string, number>();
    const shown = failures.filter(f => !isCancelled(f.error));
    for (const f of shown) {
        const t = errorText(f.error);
        texts.set(t, (texts.get(t) ?? 0) + 1);
    }
    for (const [t, n] of texts)
        ui.toast(
            'danger',
            shown.length === 1 || texts.size === 1 ? (n > 1 ? `${t} (${n} objects)` : `${refLabel(shown[0].item)}: ${t}`) : `${t} (${n})`
        );
    // Declined confirmations skipped objects: say so, so a partial bulk run is never mistaken for a full one.
    const cancelled = failures.length - shown.length;
    if (cancelled && refs.length > 1) ui.toast('info', `${cancelled} of ${refs.length} objects cancelled`);
}

/** `object.action` over refs, one `requestId` each. Reports through toasts; resolves to whether everything succeeded. */
export async function runAction(refs: readonly ObjectRef[], action: string, label: string, params?: Record<string, unknown>): Promise<boolean> {
    const contexts = new Set<string>();
    const decisions = bulkDecisions();
    const failures = await pool(refs, 4, async ref => {
        const res = await callMutating<MutationResult>(
            ref.cluster,
            'object.action',
            { requestId: newRequestId(), ref, action, params },
            { decision: decisions(ref.cluster) }
        );
        contexts.add(res.usedContext);
        if (action === 'trigger' && res.name) useUiStore().toast('info', `Created job ${res.name}`);
    });
    report(label, refs, failures, contexts);
    return failures.length === 0;
}

export async function runDelete(refs: readonly ObjectRef[], propagation?: string): Promise<boolean> {
    const contexts = new Set<string>();
    const decisions = bulkDecisions();
    const failures = await pool(refs, 4, async ref => {
        const res = await callMutating<MutationResult>(
            ref.cluster,
            'object.delete',
            { requestId: newRequestId(), ref, propagation },
            { decision: decisions(ref.cluster) }
        );
        contexts.add(res.usedContext);
    });
    report('Deleted', refs, failures, contexts);
    return failures.length === 0;
}
