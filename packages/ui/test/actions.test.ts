import { describe, expect, it } from 'vitest';

import { accessCheckFor, actionById, actionsFor, actionState, confirmPhrase, needsTypeToConfirm, newRequestId } from '../src/lib/actions.ts';

describe('actionsFor', () => {
    it('offers kind-specific actions and always Delete last', () => {
        expect(actionsFor('apps/v1/deployments').map(a => a.id)).toEqual(['scale', 'restart', 'rollback', 'delete']);
        expect(actionsFor('apps/v1/daemonsets').map(a => a.id)).toEqual(['restart', 'delete']);
        expect(actionsFor('v1/nodes').map(a => a.id)).toEqual(['cordon', 'uncordon', 'drain', 'nodeshell', 'delete']);
        expect(actionsFor('batch/v1/cronjobs').map(a => a.id)).toEqual(['suspend', 'resume', 'trigger', 'delete']);
        expect(actionsFor('v1/configmaps').map(a => a.id)).toEqual(['delete']);
    });

    it('marks which actions work on a multi-selection', () => {
        const bulk = actionsFor('apps/v1/deployments')
            .filter(a => a.bulk)
            .map(a => a.id);
        expect(bulk).toEqual(['restart', 'delete']);
    });

    it('flags parameter dialogs and danger', () => {
        expect(actionById('apps/v1/deployments', 'scale')?.param).toBe('scale');
        expect(actionById('apps/v1/deployments', 'rollback')?.param).toBe('rollback');
        expect(actionById('v1/pods', 'delete')?.danger).toBe(true);
        expect(actionById('v1/pods', 'scale')).toBeUndefined();
    });
});

describe('accessCheckFor', () => {
    const ref = { cluster: 'c', gvr: 'apps/v1/deployments', namespace: 'ns', name: 'api' };
    it('checks the object itself for ordinary actions', () => {
        expect(accessCheckFor(actionById(ref.gvr, 'restart')!, ref)).toEqual({
            cluster: 'c',
            verb: 'patch',
            gvr: ref.gvr,
            namespace: 'ns',
            name: 'api'
        });
        expect(accessCheckFor(actionById(ref.gvr, 'delete')!, ref).verb).toBe('delete');
    });
    it('checks create on jobs for trigger, without a name', () => {
        const cj = { cluster: 'c', gvr: 'batch/v1/cronjobs', namespace: 'ns', name: 'nightly' };
        expect(accessCheckFor(actionById(cj.gvr, 'trigger')!, cj)).toEqual({
            cluster: 'c',
            verb: 'create',
            gvr: 'batch/v1/jobs',
            namespace: 'ns',
            name: undefined
        });
    });
});

describe('actionState', () => {
    it('is enabled while access is unknown', () => {
        expect(actionState([])).toEqual({ disabled: false, reason: '', viaWrite: false });
        expect(actionState([undefined])).toMatchObject({ disabled: false });
    });
    it('disables with the first denial reason', () => {
        const s = actionState([
            { allowed: true, viaWrite: false, reason: '' },
            { allowed: false, viaWrite: false, reason: "You can't delete pods in payments" }
        ]);
        expect(s).toEqual({ disabled: true, reason: "You can't delete pods in payments", viaWrite: false });
        expect(actionState([{ allowed: false, viaWrite: false, reason: '' }]).reason).toBe('Not permitted');
    });
    it('hints the write context when any target needs it', () => {
        const s = actionState([
            { allowed: true, viaWrite: false, reason: '' },
            { allowed: true, viaWrite: true, reason: '' }
        ]);
        expect(s.disabled).toBe(false);
        expect(s.viaWrite).toBe(true);
        expect(s.reason).toMatch(/write context/);
    });
    it('keeps an action that needs the write context after confirmation enabled', () => {
        const s = actionState([
            { allowed: false, viaWrite: false, needsConfirm: true, reason: 'allowed through the linked write context after confirmation' }
        ]);
        expect(s).toMatchObject({ disabled: false, viaWrite: true });
    });
    it("shows the agent's note when the write context could not be checked", () => {
        const note = 'runs through the write context prod-admin, which could not be checked (is its elevated session active?)';
        expect(actionState([{ allowed: true, viaWrite: true, reason: note }])).toEqual({ disabled: false, reason: note, viaWrite: true });
    });
});

describe('needsTypeToConfirm', () => {
    const base = {
        count: 1,
        clusters: ['a'],
        protectedClusters: ['prod']
    };
    it('single object on a normal cluster: plain confirm', () => {
        expect(needsTypeToConfirm(base)).toBe(false);
    });
    it('bulk, cross-cluster, or protected: type to confirm', () => {
        expect(needsTypeToConfirm({ ...base, count: 3 })).toBe(true);
        expect(needsTypeToConfirm({ ...base, clusters: ['a', 'b'] })).toBe(true);
        expect(needsTypeToConfirm({ ...base, clusters: ['prod'] })).toBe(true);
        expect(needsTypeToConfirm({ ...base, clusters: ['prod'], protectedClusters: [] })).toBe(false);
    });
    it('phrase is the name for one object, the count for several', () => {
        expect(confirmPhrase(1, 'api')).toBe('api');
        expect(confirmPhrase(4, 'api')).toBe('4');
    });
});

describe('newRequestId', () => {
    it('returns distinct v4 uuids', () => {
        const a = newRequestId();
        expect(a).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
        expect(newRequestId()).not.toBe(a);
    });
});

describe('confirm helpers', () => {
    const base = { count: 1, clusters: ['a'], protectedClusters: [] as string[] };

    it('a destructive action on a phone is always type-to-confirm', () => {
        expect(needsTypeToConfirm({ ...base, destructive: true })).toBe(false);
        expect(needsTypeToConfirm({ ...base, destructive: true, phone: true })).toBe(true);
        expect(needsTypeToConfirm({ ...base, phone: true })).toBe(false);
    });

    it('bulk or cross-cluster detection', async () => {
        const { isBulkOrCross } = await import('../src/lib/actions.ts');
        expect(isBulkOrCross(1, 1)).toBe(false);
        expect(isBulkOrCross(2, 1)).toBe(true);
        expect(isBulkOrCross(1, 2)).toBe(true);
    });

    it('targetSummary spells out the count and cluster names', async () => {
        const { targetSummary } = await import('../src/lib/actions.ts');
        expect(targetSummary([{ namespace: 'ns', name: 'web' }], ['Prod'])).toBe('ns/web on Prod');
        expect(targetSummary([{ name: 'n1' }, { name: 'n2' }, { name: 'n3' }], ['A', 'B'])).toBe('3 objects across 2 clusters (A, B)');
    });
});
