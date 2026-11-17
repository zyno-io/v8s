import type { Row } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import { canRollbackTo, helmStatusTone, latestRevisionOptions, mergeRow, parseManagedBy, ReleaseDetails, releaseKey } from '../src/lib/helm.ts';

const hist = [
    { revision: 1, status: 'superseded', updated: 1 },
    { revision: 3, status: 'deployed', updated: 3 },
    { revision: 2, status: 'failed', updated: 2, chart: 'podinfo-6.0.0' }
];

describe('helm status', () => {
    it('maps statuses to tones', () => {
        expect(helmStatusTone('deployed')).toBe('success');
        expect(helmStatusTone('failed')).toBe('danger');
        expect(helmStatusTone('pending-install')).toBe('warning');
        expect(helmStatusTone('pending-upgrade')).toBe('warning');
        expect(helmStatusTone('pending-rollback')).toBe('release');
        expect(helmStatusTone('superseded')).toBe('info');
        for (const s of ['uninstalling', 'uninstalled', 'unknown', 'weird']) expect(helmStatusTone(s)).toBe('neutral');
    });
});

describe('helm helpers', () => {
    it('keys, managedBy, merge', () => {
        expect(releaseKey('c', 'ns', 'n', 2)).toBe('c|ns|n|2');
        expect(parseManagedBy('flux:ns/name')).toEqual({ namespace: 'ns', name: 'name' });
        expect(parseManagedBy('')).toBeNull();
        expect(parseManagedBy('flux:ns')).toBeNull();
        expect(parseManagedBy('argo:a/b')).toBeNull();
        const row = [1, 1, 'c1', 'podinfo', 'default', 3, 'deployed', 1000, 'flux:fx/podinfo', 1] as unknown as Row;
        expect(mergeRow(row)).toMatchObject({
            cluster: 'c1',
            name: 'podinfo',
            revision: 3,
            status: 'deployed',
            managedBy: 'flux:fx/podinfo',
            chart: ''
        });
        expect(mergeRow(row, { chart: 'podinfo', chartVersion: '6.1', appVersion: '6.1.0' }).chartVersion).toBe('6.1');
    });

    it('lists revisions newest first and checks rollback targets', () => {
        expect(latestRevisionOptions(hist).map(o => o.revision)).toEqual([3, 2, 1]);
        expect(latestRevisionOptions(undefined)).toEqual([]);
        expect(canRollbackTo(hist, 3, 2)).toBe(true);
        expect(canRollbackTo(hist, 3, 3)).toBe(false);
        expect(canRollbackTo(hist, 3, 9)).toBe(false);
        expect(canRollbackTo(hist, 3)(1)).toBe(true);
    });
});

const ref = (n: string, revision = 1) => ({ cluster: 'c', namespace: 'ns', name: n, revision });

function setup(concurrency = 3) {
    const calls: string[] = [];
    const pending = new Map<string, { ok: (v: { chart: string; chartVersion: string; appVersion: string }) => void; fail: () => void }>();
    const flush: (() => void)[] = [];
    const d = new ReleaseDetails(
        r => {
            calls.push(r.name);
            return new Promise((ok, fail) => pending.set(r.name, { ok, fail: () => fail(new Error('x')) }));
        },
        { concurrency, capacity: 2, schedule: fn => flush.push(fn) }
    );
    return { d, calls, pending, flush };
}
const tick = () => new Promise(r => setTimeout(r, 0));

describe('ReleaseDetails', () => {
    it('limits concurrency, dedupes and drops queued rows that scrolled away', async () => {
        const { d, calls, pending } = setup(2);
        d.setVisible([ref('a'), ref('b'), ref('c')]);
        d.setVisible([ref('a'), ref('b'), ref('c')]);
        expect(calls).toEqual(['a', 'b']);
        expect(d.state(ref('c'))).toBe('loading');
        d.setVisible([ref('a'), ref('d')]); // b still in flight, c dropped
        pending.get('a')!.ok({ chart: 'x', chartVersion: '1', appVersion: '' });
        await tick();
        expect(calls).toEqual(['a', 'b', 'd']);
        expect(d.state(ref('a'))).toMatchObject({ chart: 'x' });
    });

    it('refetches for a new revision, caches failures and evicts LRU', async () => {
        const { d, calls, pending } = setup();
        d.setVisible([ref('a', 1)]);
        pending.get('a')!.ok({ chart: 'x', chartVersion: '1', appVersion: '' });
        await tick();
        d.setVisible([ref('a', 1)]);
        expect(calls).toEqual(['a']);
        d.setVisible([ref('a', 2)]);
        expect(calls).toEqual(['a', 'a']);
        pending.get('a')!.fail();
        await tick();
        expect(d.state(ref('a', 2))).toBe('unknown');
        d.setVisible([ref('a', 2)]);
        expect(calls).toHaveLength(2);
        expect(d.size).toBe(2);
        d.setVisible([ref('z')]);
        pending.get('z')!.ok({ chart: 'z', chartVersion: '1', appVersion: '' });
        await tick();
        expect(d.size).toBe(2);
        expect(d.state(ref('a', 1))).toBe('loading');
    });

    it('bumps version once per batch', async () => {
        const { d, pending, flush } = setup();
        d.setVisible([ref('a'), ref('b')]);
        pending.get('a')!.ok({ chart: 'x', chartVersion: '1', appVersion: '' });
        pending.get('b')!.ok({ chart: 'y', chartVersion: '1', appVersion: '' });
        await tick();
        expect(flush).toHaveLength(1);
        flush[0]();
        expect(d.version.value).toBe(1);
    });
});
