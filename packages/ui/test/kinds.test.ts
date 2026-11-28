import type { Kind } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import { buildSidebar, kindLabel, labelFromGvr, sectionFor } from '../src/lib/kinds.ts';

const k = (gvr: string, kind: string, plural: string, extra: Partial<Kind> = {}): Kind => {
    const parts = gvr.split('/');
    const group = parts.length === 3 ? parts[0] : '';
    return {
        gvr,
        group,
        version: parts[parts.length - 2],
        kind,
        plural,
        shortNames: [],
        namespaced: true,
        available: true,
        curated: false,
        clusters: ['a'],
        ...extra
    };
};

const kinds: Kind[] = [
    k('v1/pods', 'Pod', 'pods'),
    k('apps/v1/deployments', 'Deployment', 'deployments'),
    k('v1/nodes', 'Node', 'nodes', { namespaced: false }),
    k('v1/configmaps', 'ConfigMap', 'configmaps'),
    k('v1/services', 'Service', 'services'),
    k('v1/persistentvolumeclaims', 'PersistentVolumeClaim', 'persistentvolumeclaims'),
    k('rbac.authorization.k8s.io/v1/roles', 'Role', 'roles'),
    k('kustomize.toolkit.fluxcd.io/v1/kustomizations', 'Kustomization', 'kustomizations'),
    k('helm.toolkit.fluxcd.io/v2/helmreleases', 'HelmRelease', 'helmreleases'),
    k('coordination.k8s.io/v1/leases', 'Lease', 'leases'),
    k('example.io/v1/hidden', 'Hidden', 'hidden', { available: false }),
    k('example.io/v1/widgets', 'Widget', 'widgets'),
    k('helm.sh/v1/releases', 'Release', 'releases')
];

describe('sectionFor', () => {
    it('maps well-known kinds to sections and unknown groups to custom resources', () => {
        expect(sectionFor(kinds[0])).toBe('workloads');
        expect(sectionFor(kinds[2])).toBe('cluster');
        expect(sectionFor(kinds[3])).toBe('config');
        expect(sectionFor(kinds[4])).toBe('network');
        expect(sectionFor(kinds[5])).toBe('storage');
        expect(sectionFor(kinds[6])).toBe('access');
        expect(sectionFor(kinds[7])).toBe('flux');
        expect(sectionFor(kinds[12])).toBe('helm');
        expect(sectionFor(kinds[11])).toBe('crd');
        expect(sectionFor(kinds[9])).toBe('more');
    });
});

describe('buildSidebar', () => {
    const m = buildSidebar(kinds, ['apps/v1/deployments', 'gone/v1/things']);

    it('lists starred first, keeping stars that are not available', () => {
        expect(m.starred).toEqual([
            { gvr: 'apps/v1/deployments', label: 'Deployments' },
            { gvr: 'gone/v1/things', label: 'Things' }
        ]);
    });

    it('orders sections and items', () => {
        expect(m.sections.map(s => s.id)).toEqual(['cluster', 'workloads', 'network', 'config', 'storage', 'access', 'flux', 'helm', 'more']);
        expect(m.sections.find(s => s.id === 'flux')!.items.map(i => i.label)).toEqual([
            'Overview',
            'Dependency graph',
            'Kustomizations',
            'Helmreleases'
        ]);
        expect(m.sections.find(s => s.id === 'workloads')!.items.map(i => i.label)).toEqual(['Pods', 'Deployments']);
    });

    it('groups custom resources by API group and hides unavailable kinds', () => {
        expect(m.crds.map(g => g.group)).toEqual(['example.io']);
        expect(m.crds.flatMap(g => g.items.map(i => i.gvr))).not.toContain('example.io/v1/hidden');
    });
});

describe('labels', () => {
    it('capitalizes plurals', () => {
        expect(kindLabel(kinds[0])).toBe('Pods');
        expect(labelFromGvr('batch/v1/cronjobs')).toBe('Cronjobs');
    });
});

describe('kind resolution', () => {
    const core = k('v1/events', 'Event', 'events', { curated: true, shortNames: ['ev'] });
    const ev = k('events.k8s.io/v1/events', 'Event', 'events', { shortNames: ['ev'] });
    const list: Kind[] = [ev, core, k('v1/pods', 'Pod', 'pods')];

    it('resolveKind prefers the group, then curated, then core', async () => {
        const { resolveKind } = await import('../src/lib/kinds.ts');
        expect(resolveKind(list, 'Event')?.gvr).toBe('v1/events');
        expect(resolveKind(list, 'Event', 'events.k8s.io')?.gvr).toBe('events.k8s.io/v1/events');
        expect(resolveKind([ev, k('v1/events', 'Event', 'events')], 'Event')?.gvr).toBe('v1/events');
        expect(resolveKind(list, 'Nope')).toBeUndefined();
    });

    it('hides events.k8s.io events only while core events exist', async () => {
        const { visibleKinds } = await import('../src/lib/kinds.ts');
        expect(visibleKinds(list).map(x => x.gvr)).toEqual(['v1/events', 'v1/pods']);
        expect(visibleKinds([ev]).map(x => x.gvr)).toEqual(['events.k8s.io/v1/events']);
        expect(
            buildSidebar(list, [])
                .sections.flatMap(s => s.items)
                .map(i => i.gvr)
        ).not.toContain('events.k8s.io/v1/events');
    });

    it(':ev resolves to the core events', async () => {
        const { findKind } = await import('../src/lib/command-parser.ts');
        const hit = findKind('ev', list);
        expect(hit && 'kind' in hit && hit.kind.gvr).toBe('v1/events');
    });
});
