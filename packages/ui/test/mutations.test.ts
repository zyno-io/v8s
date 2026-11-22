import { RpcError } from '@v8s/client';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const call = vi.fn();
const clusterStates = vi.hoisted(() => ({ value: [] as { id: string; phase: string; synced: number; elevated?: boolean }[] }));
vi.mock('../src/lib/agent.ts', () => ({
    getAgent: () => ({ call, clusterStates }),
    refreshKinds: () => {}
}));

import { bulkDecisions, callMutating, errorText, isCancelled, newWriteDecision, runAction, runAs } from '../src/lib/mutations.ts';
import { useClustersStore } from '../src/stores/clusters.ts';
import { useUiStore } from '../src/stores/ui.ts';

beforeEach(() => {
    setActivePinia(createPinia());
    call.mockReset();
    clusterStates.value = [];
    useClustersStore().list = [{ id: 'prod', name: 'Prod', server: '', groups: [], writeContext: 'prod-admin' }];
});

const tick = () => new Promise(r => setTimeout(r, 0));

describe('callMutating', () => {
    it('passes a plain success through', async () => {
        call.mockResolvedValue({ ok: 1 });
        await expect(callMutating('prod', 'object.delete', { requestId: 'r' })).resolves.toEqual({ ok: 1 });
        expect(call).toHaveBeenCalledTimes(1);
    });

    it('write_confirm: asks, then retries once with useWriteContext and a FRESH requestId', async () => {
        call.mockRejectedValueOnce(new RpcError('write_confirm', 'confirm')).mockResolvedValueOnce({ usedContext: 'prod-admin' });
        const p = callMutating('prod', 'object.delete', { requestId: 'r1' });
        await tick();
        const ui = useUiStore();
        expect(ui.dialog?.type).toBe('confirm');
        expect(ui.dialog && 'title' in ui.dialog && ui.dialog.title).toContain('prod-admin');
        if (ui.dialog?.type === 'confirm') await ui.dialog.run();
        await expect(p).resolves.toEqual({ usedContext: 'prod-admin' });
        const retry = call.mock.calls[1][1] as { requestId: string; useWriteContext: boolean };
        expect(retry.useWriteContext).toBe(true);
        expect(retry.requestId).not.toBe('r1');
    });

    /** An agent that de-duplicates by requestId and caches errors too: a replayed id returns the cached refusal. */
    function dedupingAgent() {
        const cache = new Map<string, unknown>();
        call.mockImplementation(async (_m: string, p: { requestId: string; useWriteContext?: boolean }) => {
            if (p.requestId && cache.has(p.requestId)) {
                const c = cache.get(p.requestId);
                if (c instanceof RpcError) throw c;
                return c;
            }
            if (!p.useWriteContext) {
                const err = new RpcError('write_confirm', 'confirm');
                if (p.requestId) cache.set(p.requestId, err);
                throw err;
            }
            const ok = { usedContext: 'prod-admin' };
            if (p.requestId) cache.set(p.requestId, ok);
            return ok;
        });
    }
    const accept = async () => {
        await tick();
        const ui = useUiStore();
        if (ui.dialog?.type === 'confirm') await ui.dialog.run();
        ui.dialog = null;
    };

    it('against an agent that caches errors by requestId, the write_confirm retry still succeeds', async () => {
        dedupingAgent();
        const adopted: string[] = [];
        const p = callMutating('prod', 'object.action', { requestId: 'r1' }, { onRequestId: id => adopted.push(id) });
        await accept();
        await expect(p).resolves.toEqual({ usedContext: 'prod-admin' });
        expect(adopted).toHaveLength(1);
        expect(adopted[0]).not.toBe('r1');
    });

    it('a carried decision sends useWriteContext up front (dry run then apply: asked once, no second refusal)', async () => {
        dedupingAgent();
        const d = newWriteDecision();
        const dry = callMutating('prod', 'object.dryRun', {}, { decision: d });
        await accept();
        await dry;
        expect(d.accepted).toBe(true);
        call.mockClear();
        await expect(callMutating('prod', 'object.apply', { requestId: 'a1' }, { decision: d })).resolves.toEqual({ usedContext: 'prod-admin' });
        expect(call).toHaveBeenCalledTimes(1);
        expect(call).toHaveBeenCalledWith('object.apply', { requestId: 'a1', useWriteContext: true });
        expect(useUiStore().dialog).toBeNull();
    });

    it('write_confirm declined: rejects with a cancellation and does not retry', async () => {
        call.mockRejectedValueOnce(new RpcError('write_confirm', 'confirm'));
        const p = callMutating('prod', 'object.delete', {});
        await tick();
        const ui = useUiStore();
        if (ui.dialog?.type === 'confirm') ui.dialog.onCancel?.();
        const err = await p.catch(e => e);
        expect(isCancelled(err)).toBe(true);
        expect(call).toHaveBeenCalledTimes(1);
    });

    it('link_mismatch: explains, opens the cluster selector when confirmed, and rethrows', async () => {
        call.mockRejectedValueOnce(new RpcError('link_mismatch', 'mismatch'));
        const p = callMutating('prod', 'object.apply', {});
        const settled = p.catch(e => e);
        await tick();
        const ui = useUiStore();
        expect(ui.dialog?.type === 'confirm' && ui.dialog.confirmLabel).toBe('Open cluster selector');
        if (ui.dialog?.type === 'confirm') ui.dialog.run();
        const err = await settled;
        expect((err as RpcError).code).toBe('link_mismatch');
        expect(useClustersStore().popoverOpen).toBe(true);
    });
});

describe('errorText and runAs', () => {
    it('appends the server hint', () => {
        expect(errorText(new RpcError('gitops_managed', 'managed by Flux', undefined, 'Use reconcile.'))).toBe('managed by Flux Use reconcile.');
    });
    it('names the write context or the current one', () => {
        expect(runAs('prod', true)).toBe('Runs as prod-admin');
        expect(runAs('prod', false)).toBe('Runs as your current context');
    });

    it('runAs says (elevated) while the cluster is elevated', () => {
        const clusters = useClustersStore();
        clusters.loaded = true;
        clusterStates.value = [{ id: 'prod', phase: 'ready', synced: 0, elevated: true }];
        expect(runAs('prod', false)).toBe('Runs as prod-admin (elevated)');
        expect(runAs('prod', true)).toBe('Runs as prod-admin (elevated)');
    });
});

describe('bulk write confirm', () => {
    const refs = Array.from({ length: 9 }, (_, i) => ({ cluster: 'prod', gvr: 'apps/v1/deployments', namespace: 'ns', name: `d${i}` }));

    it('asks once per cluster for the whole run and passes the answer to every item', async () => {
        call.mockImplementation(async (_m: string, p: { useWriteContext?: boolean }) => {
            if (!p.useWriteContext) throw new RpcError('write_confirm', 'confirm');
            return { usedContext: 'prod-admin' };
        });
        const ui = useUiStore();
        let dialogs = 0;
        const run = runAction(refs, 'restart', 'Restarted');
        for (let i = 0; i < 20; i++) {
            await tick();
            if (ui.dialog?.type === 'confirm') {
                dialogs++;
                await ui.dialog.run();
                ui.dialog = null;
            }
        }
        await expect(run).resolves.toBe(true);
        expect(dialogs).toBe(1);
    });

    it('declining once cancels the rest without calling the agent, and the summary counts them', async () => {
        call.mockImplementation(async () => {
            throw new RpcError('write_confirm', 'confirm');
        });
        const ui = useUiStore();
        let dialogs = 0;
        const run = runAction(refs, 'restart', 'Restarted');
        for (let i = 0; i < 20; i++) {
            await tick();
            if (ui.dialog?.type === 'confirm') {
                dialogs++;
                ui.dialog.onCancel?.();
                ui.dialog = null;
            }
        }
        await expect(run).resolves.toBe(false);
        expect(dialogs).toBe(1);
        expect(call.mock.calls.length).toBeLessThanOrEqual(4);
        expect(ui.toasts.some(t => t.text === '9 of 9 objects cancelled')).toBe(true);
    });

    it('bulkDecisions hands out one decision per cluster', () => {
        const d = bulkDecisions();
        expect(d('a')).toBe(d('a'));
        expect(d('a')).not.toBe(d('b'));
    });
});
