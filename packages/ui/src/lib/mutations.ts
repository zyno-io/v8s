import type { MutationResult, ObjectRef } from '@v8s/client';

import { RpcError } from '@v8s/client';

import { useUiStore } from '@/stores/ui.ts';

import { newRequestId } from './actions.ts';
import { getAgent } from './agent.ts';

export const WRITE_UNAVAILABLE_TEXT = 'The write context was rejected — is your elevated session active?';

/** Human text for a failed request. `write_unavailable` leads with the standard hint, then the server's own message. */
export function errorText(e: unknown): string {
    if (e instanceof RpcError) {
        if (e.code === 'write_unavailable') return e.message ? `${WRITE_UNAVAILABLE_TEXT} ${e.message}` : WRITE_UNAVAILABLE_TEXT;
        return e.message || e.code;
    }
    return e instanceof Error ? e.message : String(e);
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
    for (const f of failures) {
        const t = errorText(f.error);
        texts.set(t, (texts.get(t) ?? 0) + 1);
    }
    for (const [t, n] of texts)
        ui.toast(
            'danger',
            failures.length === 1 || texts.size === 1 ? (n > 1 ? `${t} (${n} objects)` : `${refLabel(failures[0].item)}: ${t}`) : `${t} (${n})`
        );
}

/** `object.action` over refs, one `requestId` each. Reports through toasts; resolves to whether everything succeeded. */
export async function runAction(refs: readonly ObjectRef[], action: string, label: string, params?: Record<string, unknown>): Promise<boolean> {
    const contexts = new Set<string>();
    const failures = await pool(refs, 4, async ref => {
        const res = await getAgent().call<MutationResult>('object.action', { requestId: newRequestId(), ref, action, params });
        contexts.add(res.usedContext);
        if (action === 'trigger' && res.name) useUiStore().toast('info', `Created job ${res.name}`);
    });
    report(label, refs, failures, contexts);
    return failures.length === 0;
}

export async function runDelete(refs: readonly ObjectRef[], propagation?: string): Promise<boolean> {
    const contexts = new Set<string>();
    const failures = await pool(refs, 4, async ref => {
        const res = await getAgent().call<MutationResult>('object.delete', { requestId: newRequestId(), ref, propagation });
        contexts.add(res.usedContext);
    });
    report('Deleted', refs, failures, contexts);
    return failures.length === 0;
}
