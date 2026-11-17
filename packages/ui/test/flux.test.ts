import { afterEach, describe, expect, it, vi } from 'vitest';

import { fluxActionDefs } from '../src/lib/flux-actions.ts';
import { fluxDecor } from '../src/lib/flux-decor.ts';
import {
    aggregateOverview,
    baseFromColumns,
    clearPending,
    fluxKindOf,
    isFluxGvr,
    isStale,
    objectKey,
    parseInterval,
    pendingReconciles,
    pendingToken,
    readCells,
    readyTag,
    reconcileState,
    RECONCILE_EXPIRY_MS,
    settle,
    shortRevision,
    sweep,
    trackReconcile,
    truncateMessage,
    version
} from '../src/lib/flux.ts';

// [id, ver, cluster, name, namespace, ready, suspended, revision, source, interval, lastReconcile, message, requestedAt, lastHandled, dependsOn, tone, createdAt]
function row(o: Partial<Record<string, string | number>> = {}) {
    const d: Record<string, string | number> = {
        cluster: 'c1',
        name: 'apps',
        namespace: 'flux-system',
        ready: 'True',
        suspended: '',
        revision: 'main@abcdef1',
        source: 'GitRepository/flux-system',
        interval: '10m',
        lastReconcile: 1000,
        message: 'Applied',
        requestedAt: '',
        lastHandled: '',
        dependsOn: '',
        statusTone: 1,
        createdAt: 5,
        ...o
    };
    return [
        1,
        1,
        d.cluster,
        d.name,
        d.namespace,
        d.ready,
        d.suspended,
        d.revision,
        d.source,
        d.interval,
        d.lastReconcile,
        d.message,
        d.requestedAt,
        d.lastHandled,
        d.dependsOn,
        d.statusTone,
        d.createdAt
    ] as const;
}

afterEach(() => {
    clearPending();
    vi.useRealTimers();
});

describe('kind registry', () => {
    it('is version tolerant and matches group + plural', () => {
        expect(fluxKindOf('source.toolkit.fluxcd.io/v1/gitrepositories')?.kind).toBe('GitRepository');
        expect(fluxKindOf('source.toolkit.fluxcd.io/v1beta2/gitrepositories')?.kind).toBe('GitRepository');
        expect(fluxKindOf('kustomize.toolkit.fluxcd.io/v1/kustomizations')?.id).toBe('kustomization');
        expect(fluxKindOf('helm.toolkit.fluxcd.io/v2beta2/helmreleases')?.id).toBe('helmrelease');
        expect(fluxKindOf('source.toolkit.fluxcd.io/v1/buckets')?.source).toBe(true);
        expect(fluxKindOf('helm.toolkit.fluxcd.io/v2/helmreleases')?.source).toBe(false);
    });
    it('rejects other groups and plurals', () => {
        expect(isFluxGvr('apps/v1/deployments')).toBe(false);
        expect(isFluxGvr('v1/pods')).toBe(false);
        expect(isFluxGvr('source.toolkit.fluxcd.io/v1/externalartifacts')).toBe(false);
        expect(isFluxGvr('helm.sh/v1/releases')).toBe(false);
        expect(isFluxGvr('nonsense')).toBe(false);
    });
});

describe('row reading', () => {
    it('reads cells by column key', () => {
        const c = readCells(row({ suspended: 'true', statusTone: 0, lastReconcile: 42 }));
        expect(c).toMatchObject({
            cluster: 'c1',
            name: 'apps',
            namespace: 'flux-system',
            suspended: true,
            tone: 0,
            lastReconcile: 42,
            interval: '10m'
        });
    });
    it('finds the base from a schema without the cluster column offset', () => {
        expect(baseFromColumns([{ key: 'cluster' }, { key: 'name' }, { key: 'namespace' }, { key: 'ready' }])).toBe(2);
        expect(baseFromColumns([{ key: 'name' }, { key: 'namespace' }, { key: 'ready' }])).toBe(1);
        expect(baseFromColumns(null)).toBe(2);
    });
    it('honours a shifted base', () => {
        const r = [0, 0, ...row().slice(2)];
        const shifted = [9, 9, 9, ...r.slice(2)];
        expect(readCells(shifted, 3).name).toBe('apps');
    });
});

describe('readyTag', () => {
    it('Ready', () => expect(readyTag(row())).toEqual({ text: 'Ready', tone: 'success' }));
    it('keeps the reason', () =>
        expect(readyTag(row({ ready: 'False · ArtifactFailed', statusTone: 3 }))).toEqual({ text: 'Not ready · ArtifactFailed', tone: 'danger' }));
    it('progressing not-ready is warning via the tone', () =>
        expect(readyTag(row({ ready: 'False · Progressing', statusTone: 2 }))).toEqual({ text: 'Not ready · Progressing', tone: 'warning' }));
    it('Unknown and empty', () => {
        expect(readyTag(row({ ready: 'Unknown', statusTone: 2 }))).toEqual({ text: 'Unknown', tone: 'warning' });
        expect(readyTag(row({ ready: '', statusTone: 0 }))).toEqual({ text: 'Unknown', tone: 'warning' });
    });
    it('suspended is neutral', () => expect(readyTag(row({ suspended: 'true', statusTone: 0 }))).toEqual({ text: 'Suspended', tone: 'neutral' }));
    it('False without a reason', () => expect(readyTag(row({ ready: 'False', statusTone: 3 })).text).toBe('Not ready'));
});

describe('text helpers', () => {
    it('shortens git revisions', () => {
        expect(shortRevision('main@sha1:abcdef0123456789abcdef0123456789abcdef01')).toBe('main@abcdef0');
        expect(shortRevision('main@abcdef0123456789abcdef0123456789abcdef01')).toBe('main@abcdef0');
        expect(shortRevision('latest@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')).toBe('latest@0123456');
    });
    it('leaves chart versions alone', () => {
        expect(shortRevision('6.5.4')).toBe('6.5.4');
        expect(shortRevision('1.2.3-rc.1+build5')).toBe('1.2.3-rc.1+build5');
        expect(shortRevision('')).toBe('');
    });
    it('truncates messages and collapses whitespace', () => {
        expect(truncateMessage('short')).toBe('short');
        expect(truncateMessage('a\n  b')).toBe('a b');
        const t = truncateMessage('x'.repeat(300), 50);
        expect(t.length).toBe(50);
        expect(t.endsWith('…')).toBe(true);
    });
    it('parses intervals', () => {
        expect(parseInterval('10m')).toBe(600_000);
        expect(parseInterval('1h')).toBe(3_600_000);
        expect(parseInterval('1h30m')).toBe(5_400_000);
        expect(parseInterval('30s')).toBe(30_000);
        expect(parseInterval('500ms')).toBe(500);
        expect(parseInterval('')).toBe(0);
        expect(parseInterval('soon')).toBe(0);
    });
    it('detects staleness at 3x the interval', () => {
        const now = 10_000_000;
        expect(isStale('10m', now - 31 * 60_000, now)).toBe(true);
        expect(isStale('10m', now - 29 * 60_000, now)).toBe(false);
        expect(isStale('', now - 99_999_999, now)).toBe(false);
        expect(isStale('10m', 0, now)).toBe(false);
    });
});

describe('reconcileState', () => {
    const now = Date.parse('2026-11-17T12:00:00Z');
    const t = '2026-11-17T11:59:00Z';
    it('idle with nothing requested', () => expect(reconcileState(row(), undefined, 2, now)).toBe('idle'));
    it('reconciling with a pending token not yet handled', () =>
        expect(reconcileState(row({ lastHandled: 'old' }), 'tok', 2, now)).toBe('reconciling'));
    it('resolved when the token was handled', () => expect(reconcileState(row({ lastHandled: 'tok' }), 'tok', 2, now)).toBe('resolved'));
    it('reconciling from requestedAt when it differs from lastHandled', () =>
        expect(reconcileState(row({ requestedAt: t, lastHandled: 'x' }), undefined, 2, now)).toBe('reconciling'));
    it('idle when requestedAt equals lastHandled', () =>
        expect(reconcileState(row({ requestedAt: t, lastHandled: t }), undefined, 2, now)).toBe('idle'));
    it('a stale requestedAt does not show reconciling forever', () =>
        expect(reconcileState(row({ requestedAt: '2026-11-17T11:00:00Z' }), undefined, 2, now)).toBe('idle'));
    it('suspended never reconciles from requestedAt', () =>
        expect(reconcileState(row({ requestedAt: t, suspended: 'true' }), undefined, 2, now)).toBe('idle'));
});

describe('pending store', () => {
    it('keys objects', () => expect(objectKey({ cluster: 'c', gvr: 'g/v1/x', namespace: 'ns', name: 'n' })).toBe('c|g/v1/x|ns|n'));
    it('tracks, bumps the version and settles on the token', () => {
        const v0 = version.value;
        trackReconcile('k', 'tok');
        expect(version.value).toBe(v0 + 1);
        expect(pendingToken('k')).toBe('tok');
        expect(settle('k', 'other')).toBe(false);
        expect(pendingReconciles.has('k')).toBe(true);
        expect(settle('k', 'tok')).toBe(true);
        expect(pendingReconciles.has('k')).toBe(false);
        expect(version.value).toBe(v0 + 2);
    });
    it('ignores a missing token', () => {
        const v0 = version.value;
        trackReconcile('k', undefined);
        expect(pendingReconciles.size).toBe(0);
        expect(version.value).toBe(v0);
    });
    it('expires after 5 minutes', () => {
        trackReconcile('k', 'tok', 1000);
        expect(pendingToken('k', 1000 + RECONCILE_EXPIRY_MS)).toBe('tok');
        expect(pendingToken('k', 1000 + RECONCILE_EXPIRY_MS + 1)).toBeUndefined();
        expect(sweep(1000 + RECONCILE_EXPIRY_MS + 1)).toBe(1);
        expect(pendingReconciles.size).toBe(0);
    });
    it('sweeps by timer', () => {
        vi.useFakeTimers();
        trackReconcile('k', 'tok');
        vi.advanceTimersByTime(RECONCILE_EXPIRY_MS + 100);
        expect(pendingReconciles.size).toBe(0);
    });
});

describe('aggregateOverview', () => {
    it('sums and derives the other bucket', () => {
        const o = aggregateOverview({
            kustomization: { total: 10, ready: 6, notready: 2, suspended: 1 },
            helmrelease: { total: 4, ready: 4, notready: 0, suspended: 0 }
        });
        expect(o.rows.kustomization.other).toBe(1);
        expect(o.totals).toEqual({ total: 14, ready: 10, notready: 2, suspended: 1, other: 1 });
        expect(o.healthy).toBe(false);
    });
    it('is healthy with no failures and never negative', () => {
        const o = aggregateOverview({ bucket: { total: 1, ready: 1, notready: 0, suspended: 1 } });
        expect(o.rows.bucket.other).toBe(0);
        expect(o.healthy).toBe(true);
        expect(aggregateOverview({}).totals.total).toBe(0);
    });
});

describe('fluxActionDefs', () => {
    const ids = (g: string) => fluxActionDefs(g).map(a => a.id);
    it('sources', () => expect(ids('source.toolkit.fluxcd.io/v1/gitrepositories')).toEqual(['flux.suspend', 'flux.resume', 'flux.reconcile']));
    it('kustomizations', () =>
        expect(ids('kustomize.toolkit.fluxcd.io/v1/kustomizations')).toEqual([
            'flux.suspend',
            'flux.resume',
            'flux.reconcile',
            'flux.reconcileWithSource'
        ]));
    it('helmreleases', () =>
        expect(ids('helm.toolkit.fluxcd.io/v2/helmreleases')).toEqual([
            'flux.suspend',
            'flux.resume',
            'flux.reconcile',
            'flux.reconcileWithSource',
            'flux.force',
            'flux.reset'
        ]));
    it('others get none and all are bulk patch', () => {
        expect(ids('apps/v1/deployments')).toEqual([]);
        expect(fluxActionDefs('helm.toolkit.fluxcd.io/v2/helmreleases').every(a => a.bulk && a.verb === 'patch' && a.param === null)).toBe(true);
    });
});

describe('fluxDecor', () => {
    const gvr = 'kustomize.toolkit.fluxcd.io/v1/kustomizations';
    const col = (key: string, idx: number) => ({ key, idx }) as never;
    const d = fluxDecor(gvr)!;
    it('exists only for Flux kinds', () => {
        expect(fluxDecor('v1/pods')).toBeNull();
        expect(d.defaultHidden).toEqual(['requestedAt', 'lastHandled', 'dependsOn']);
    });
    it('renders ready, suspended, revision, message', () => {
        expect(d.cell!(col('ready', 5), row() as never)?.tag).toEqual({ text: 'Ready', tone: 'success' });
        expect(d.cell!(col('suspended', 6), row({ suspended: 'true', statusTone: 0 }) as never)?.tag?.text).toBe('Suspended');
        expect(d.cell!(col('suspended', 6), row() as never)?.tag).toBeUndefined();
        expect(d.cell!(col('revision', 7), row({ revision: 'main@sha1:abcdef0123456789' }) as never)).toMatchObject({
            mono: true,
            text: 'main@abcdef0'
        });
        const m = d.cell!(col('message', 11), row({ message: 'x'.repeat(500) }) as never)!;
        expect(m.text!.length).toBeLessThan(200);
        expect(m.title!.length).toBe(500);
        expect(d.cell!(col('lastReconcile', 10), row() as never)).toBeNull();
    });
    it('shows Reconciling… for a pending token and clears on settle', () => {
        const r = row({ lastHandled: 'old' });
        trackReconcile(objectKey({ cluster: 'c1', gvr, namespace: 'flux-system', name: 'apps' }), 'tok');
        expect(d.cell!(col('ready', 5), r as never)?.tag).toEqual({ text: 'Reconciling…', tone: 'warning' });
        expect(d.rowKey!(r as never)).toBe(1);
        const handled = row({ lastHandled: 'tok' });
        d.visible!([handled as never]);
        expect(d.cell!(col('ready', 5), handled as never)?.tag?.text).toBe('Ready');
        expect(d.rowKey!(handled as never)).toBe(0);
    });
});
