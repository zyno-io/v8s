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
    if (!viaWrite) return 'Runs as your current context';
    const ctx = useClustersStore().writeContextOf(cluster);
    return `Runs as ${ctx || 'the linked write context'}`;
}

/** One dialog per (kind, cluster) at a time: a bulk action's parallel items share the answer instead of stacking dialogs. */
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
 * Runs `fn` (any call that accepts `useWriteContext`: a request or `openStream`) with the two context-routing errors handled
 * (docs/protocol.md, Mutations): `write_confirm` asks the user and retries once with `useWriteContext: true`; `link_mismatch`
 * explains that nothing was written and offers the cluster selector. Declining rejects with `cancelled`.
 */
export async function withWriteConfirm<T>(cluster: string, fn: (extra: { useWriteContext?: true }) => Promise<T>): Promise<T> {
    try {
        return await fn({});
    } catch (e) {
        if (!(e instanceof RpcError)) throw e;
        const clusters = useClustersStore();
        if (e.code === 'write_confirm') {
            const ctx = clusters.writeContextOf(cluster) || 'the linked write context';
            const name = clusters.nameById.get(cluster) ?? cluster;
            const ok = await once(`confirm|${cluster}`, () =>
                ask({
                    title: `Run as ${ctx}?`,
                    body: `Your current context cannot do this on ${name}. Run it through the linked write context ${ctx} instead? Runs as ${ctx}.`,
                    confirmLabel: 'Run'
                })
            );
            if (!ok) throw new RpcError('cancelled', 'Cancelled');
            return await fn({ useWriteContext: true });
        }
        if (e.code === 'link_mismatch') {
            const ui = useUiStore();
            const ctx = clusters.writeContextOf(cluster) || 'The linked write context';
            const open = await once(
                `link|${cluster}`,
                () =>
                    new Promise<boolean>(resolve => {
                        ui.dialog = {
                            type: 'confirm',
                            title: 'Write context reaches a different cluster',
                            body: `${ctx} does not reach the same cluster as the read context (their kube-system namespaces differ), so nothing was written. Check the link in the cluster selector.`,
                            confirmLabel: 'Open cluster selector',
                            danger: false,
                            run: () => resolve(true),
                            onCancel: () => resolve(false)
                        };
                    })
            );
            if (open) clusters.open();
        }
        throw e;
    }
}

/** `agent.call` for a mutation through `withWriteConfirm`: the retry reuses the same params (and `requestId`). */
export function callMutating<T>(cluster: string, method: string, params: Record<string, unknown>): Promise<T> {
    return withWriteConfirm(cluster, extra => getAgent().call<T>(method, { ...params, ...extra }));
}

const refLabel = (r: ObjectRef) => `${r.namespace ? r.namespace + '/' : ''}${r.name}`;

/** Runs `fn` over items, `limit` at a time; collects errors per item. */
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
    await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
    return failures;
}

function report(verb: string, refs: readonly ObjectRef[], failures: { item: ObjectRef; error: unknown }[], contexts: Set<string>) {
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
}

/** `object.action` over refs, one `requestId` each. Reports through toasts; resolves to whether everything succeeded. */
export async function runAction(refs: readonly ObjectRef[], action: string, label: string, params?: Record<string, unknown>): Promise<boolean> {
    const contexts = new Set<string>();
    const failures = await pool(refs, 4, async ref => {
        const res = await callMutating<MutationResult>(ref.cluster, 'object.action', { requestId: newRequestId(), ref, action, params });
        contexts.add(res.usedContext);
        if (action === 'trigger' && res.name) useUiStore().toast('info', `Created job ${res.name}`);
    });
    report(label, refs, failures, contexts);
    return failures.length === 0;
}

export async function runDelete(refs: readonly ObjectRef[], propagation?: string): Promise<boolean> {
    const contexts = new Set<string>();
    const failures = await pool(refs, 4, async ref => {
        const res = await callMutating<MutationResult>(ref.cluster, 'object.delete', { requestId: newRequestId(), ref, propagation });
        contexts.add(res.usedContext);
    });
    report('Deleted', refs, failures, contexts);
    return failures.length === 0;
}
