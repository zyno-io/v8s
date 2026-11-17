import { describe, expect, it } from 'vitest';

import { remediationOf, retriesExhausted, retriesLabel } from '../src/lib/flux-helmrelease.ts';

const hr = {
    metadata: { name: 'web', namespace: 'apps', annotations: { 'reconcile.fluxcd.io/requestedAt': 't2' } },
    spec: { install: { remediation: { retries: -1 } }, upgrade: { remediation: { retries: 3, strategy: 'uninstall' } } },
    status: {
        conditions: [
            { type: 'Released', status: 'False', reason: 'UpgradeFailed', message: 'boom' },
            { type: 'Ready', status: 'False', reason: 'UpgradeFailed', message: 'boom' },
            { type: 'Other', status: 'True' }
        ],
        failures: 4,
        upgradeFailures: 4,
        lastAttemptedRevision: '1.2.3',
        lastAppliedRevision: '1.2.2',
        lastHandledReconcileAt: 't1',
        history: [{ name: 'web', namespace: 'apps', version: 7, status: 'failed', chartName: 'web', chartVersion: '1.2.3', digest: 'sha256:x' }]
    }
};

describe('remediationOf', () => {
    it('extracts conditions in order and counters against retries', () => {
        const r = remediationOf(hr);
        expect(r.conditions.map(c => c.type)).toEqual(['Ready', 'Released']);
        expect(r.failures).toBe(4);
        expect(r.install.retries).toBe(-1);
        expect(r.upgrade).toEqual({ retries: 3, strategy: 'uninstall' });
        expect(r.history[0]!.version).toBe(7);
        expect(r.reconcilePending).toBe(true);
        expect(r.releaseName).toBe('web');
    });

    it('is safe on null and empty', () => {
        const r = remediationOf(null);
        expect(r.conditions).toEqual([]);
        expect(r.upgrade.retries).toBeNull();
        expect(r.upgrade.strategy).toBe('rollback');
        expect(r.reconcilePending).toBe(false);
    });

    it('derives the release name and namespace', () => {
        expect(remediationOf({ metadata: { name: 'a', namespace: 'flux' }, spec: { targetNamespace: 't' } })).toMatchObject({
            releaseName: 't-a',
            releaseNamespace: 't'
        });
        expect(remediationOf({ metadata: { name: 'a', namespace: 'flux' }, spec: { releaseName: 'r' } })).toMatchObject({
            releaseName: 'r',
            releaseNamespace: 'flux'
        });
    });
});

describe('retries helpers', () => {
    it('labels and exhaustion', () => {
        expect(retriesLabel(-1)).toBe('unlimited');
        expect(retriesLabel(null)).toBe('0 (default)');
        expect(retriesExhausted(100, -1)).toBe(false);
        expect(retriesExhausted(4, 3)).toBe(true);
        expect(retriesExhausted(3, 3)).toBe(false);
        expect(retriesExhausted(1, null)).toBe(true);
    });
});
