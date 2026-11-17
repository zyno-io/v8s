import { RpcError } from '@v8s/client';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const call = vi.fn();
vi.mock('../src/lib/agent.ts', () => ({
    getAgent: () => ({ call, clusterStates: { value: [] } }),
    refreshKinds: () => {}
}));

import { callMutating, errorText, isCancelled, runAs } from '../src/lib/mutations.ts';
import { useClustersStore } from '../src/stores/clusters.ts';
import { useUiStore } from '../src/stores/ui.ts';

beforeEach(() => {
    setActivePinia(createPinia());
    call.mockReset();
    useClustersStore().list = [{ id: 'prod', name: 'Prod', server: '', groups: [], writeContext: 'prod-admin' }];
});

const tick = () => new Promise(r => setTimeout(r, 0));

describe('callMutating', () => {
    it('passes a plain success through', async () => {
        call.mockResolvedValue({ ok: 1 });
        await expect(callMutating('prod', 'object.delete', { requestId: 'r' })).resolves.toEqual({ ok: 1 });
        expect(call).toHaveBeenCalledTimes(1);
    });

    it('write_confirm: asks, then retries once with useWriteContext and the same requestId', async () => {
        call.mockRejectedValueOnce(new RpcError('write_confirm', 'confirm')).mockResolvedValueOnce({ usedContext: 'prod-admin' });
        const p = callMutating('prod', 'object.delete', { requestId: 'r1' });
        await tick();
        const ui = useUiStore();
        expect(ui.dialog?.type).toBe('confirm');
        expect(ui.dialog && 'title' in ui.dialog && ui.dialog.title).toContain('prod-admin');
        if (ui.dialog?.type === 'confirm') await ui.dialog.run();
        await expect(p).resolves.toEqual({ usedContext: 'prod-admin' });
        expect(call).toHaveBeenLastCalledWith('object.delete', { requestId: 'r1', useWriteContext: true });
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
});
