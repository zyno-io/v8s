import type { Kind, ObjectRef, ProblemRef, ProblemsEvidenceResult } from '@v8s/client';
import type { Router } from 'vue-router';

import { getAgent } from '@/lib/agent.ts';
import { ask } from '@/lib/ask.ts';
import { confirmGuard } from '@/lib/confirm-guard.ts';
import { openObject } from '@/lib/drawer.ts';
import { runFluxAction } from '@/lib/flux-actions.ts';
import { resolveKind } from '@/lib/kinds.ts';
import { errorText, runAction } from '@/lib/mutations.ts';
import { HELM_RELEASES_GVR, tabsFor } from '@/lib/route.ts';
import { useUiStore } from '@/stores/ui.ts';

import type { ProblemAction, ProblemRow } from './problems.ts';
import type { SnoozePreset } from './problems.ts';

import { snoozeUntil } from './problems.ts';

/** The object a problem is about, as an ObjectRef the drawer understands; null when the subject is not a cluster object. */
export function subjectRef(
    row: Pick<ProblemRow, 'cluster' | 'kind' | 'namespace' | 'name'>,
    kinds: readonly Kind[],
    ev?: ProblemsEvidenceResult | null
): ObjectRef | null {
    if (ev?.subject.gvr) return { cluster: row.cluster, gvr: ev.subject.gvr, namespace: ev.subject.namespace || undefined, name: ev.subject.name };
    if (row.kind === 'Image') return null;
    if (row.kind === 'Release') return { cluster: row.cluster, gvr: HELM_RELEASES_GVR, namespace: row.namespace || undefined, name: row.name };
    const k = resolveKind(kinds, row.kind);
    return k ? { cluster: row.cluster, gvr: k.gvr, namespace: row.namespace || undefined, name: row.name } : null;
}

/** A related object of the evidence as an ObjectRef (needs its gvr or a resolvable kind). */
export function relatedRef(cluster: string, r: ProblemRef, kinds: readonly Kind[]): ObjectRef | null {
    const gvr = r.gvr || resolveKind(kinds, r.kind)?.gvr;
    return gvr ? { cluster, gvr, namespace: r.namespace || undefined, name: r.name } : null;
}

const POD = 'Pod';

/** The pod whose logs explain a problem: the subject when it is a pod, else the first related (rolled-up) pod. */
export function logsTarget(
    cluster: string,
    subject: ObjectRef | null,
    ev: ProblemsEvidenceResult | null | undefined,
    kinds: readonly Kind[]
): ObjectRef | null {
    if (subject && tabsFor(subject.gvr).includes('logs')) return subject;
    const pod = ev?.related.find(r => r.kind === POD);
    return pod ? relatedRef(cluster, pod, kinds) : null;
}

export interface ActionCtx {
    router: Router;
    kinds: readonly Kind[];
    row: ProblemRow;
    ev?: ProblemsEvidenceResult | null;
}

export function canRun(action: ProblemAction, ctx: ActionCtx): boolean {
    const subject = subjectRef(ctx.row, ctx.kinds, ctx.ev);
    switch (action) {
        case 'logs':
        case 'previous':
            return !!logsTarget(ctx.row.cluster, subject, ctx.ev, ctx.kinds);
        case 'vulns':
            return ctx.row.kind === 'Image' || !!subject;
        case 'policy':
            return true;
        default:
            return !!subject;
    }
}

/** Runs one quick action. Mutations go through the existing action machinery (write contexts, confirmations, toasts). */
export async function runProblemAction(action: ProblemAction, ctx: ActionCtx): Promise<void> {
    const { router, row, ev, kinds } = ctx;
    const ui = useUiStore();
    const subject = subjectRef(row, kinds, ev);
    switch (action) {
        case 'logs':
        case 'previous': {
            const t = logsTarget(row.cluster, subject, ev, kinds);
            if (!t) return;
            if (action === 'previous') ui.toast('info', 'Turn on "Previous" in the Logs tab to read the last terminated container.');
            await openObject(router, t, 'logs');
            return;
        }
        case 'describe':
            if (subject) await openObject(router, subject, 'overview');
            return;
        case 'release':
            if (subject) await openObject(router, subject, subject.gvr.endsWith('helmreleases') ? 'release' : 'overview');
            return;
        case 'restart': {
            if (!subject) return;
            const ok = await ask({
                title: `Restart ${row.kind.toLowerCase()} ${row.name}?`,
                body: `Rolling restart in ${row.cluster}${row.namespace ? '/' + row.namespace : ''}.`,
                confirmLabel: 'Restart',
                hold: true,
                ...confirmGuard([subject])
            });
            if (ok) await runAction([subject], 'restart', 'Restarted');
            return;
        }
        case 'rollback': {
            if (!subject) return;
            ui.dialog = {
                type: 'rollback',
                target: subject,
                current: 0,
                ...confirmGuard([subject]),
                run: async rev => void (await runAction([subject], 'rollback', 'Rolled back', { revision: rev }))
            };
            return;
        }
        case 'reconcile': {
            if (!subject) return;
            // like the ActionBar: a production group (note) or a typed phrase needs a confirmation, otherwise it runs at once
            const guard = confirmGuard([subject]);
            if (guard.note || guard.phrase) {
                const ok = await ask({
                    title: `Reconcile ${row.name}?`,
                    body: `Flux reconciles ${row.kind} ${row.name} in ${row.cluster} now.`,
                    confirmLabel: 'Reconcile',
                    ...guard
                });
                if (!ok) return;
            }
            await runFluxAction([subject], 'flux.reconcile');
            return;
        }
        case 'suspend': {
            if (!subject) return;
            const ok = await ask({
                title: `Suspend ${row.name}?`,
                body: `Flux stops reconciling ${row.kind} ${row.name} in ${row.cluster} until it is resumed.`,
                confirmLabel: 'Suspend',
                ...confirmGuard([subject])
            });
            if (ok) await runFluxAction([subject], 'flux.suspend');
            return;
        }
        case 'policy':
            if (subject && /(kyverno|policies\.|policy)/i.test(subject.gvr) && !/pods?$/.test(subject.gvr)) {
                await router.push({
                    path: '/security/policies',
                    query: { policy: subject.gvr, c: subject.cluster, ns: subject.namespace ?? '', n: subject.name ?? '' }
                });
            } else {
                await router.push({ path: '/security/policies', query: row.name ? { policy: row.name } : {} });
            }
            return;
        case 'vulns':
            if (row.kind === 'Image') await router.push({ path: '/security', query: { image: row.name } });
            else if (subject) await openObject(router, subject, 'security');
            return;
    }
}

/** problems.ack / problems.unack with a toast on failure. Resolves to success. */
export async function setAcked(id: string, ack: boolean, snooze?: SnoozePreset): Promise<boolean> {
    try {
        if (ack) await getAgent().call('problems.ack', snooze ? { id, until: snoozeUntil(Date.now(), snooze) } : { id });
        else await getAgent().call('problems.unack', { id });
        return true;
    } catch (e) {
        useUiStore().toast('danger', `${ack ? 'Acknowledge' : 'Restore'} failed: ${errorText(e)}`);
        return false;
    }
}
