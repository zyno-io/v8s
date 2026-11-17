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
    k('example.io/v1/hidden', 'Hidden', 'hidden', { available: false })
];

describe('sectionFor', () => {
    it('maps well-known kinds to sections and unknown groups to custom resources', () => {
        expect(sectionFor(kinds[0])).toBe('workloads');
        expect(sectionFor(kinds[2])).toBe('cluster');
        expect(sectionFor(kinds[3])).toBe('config');
        expect(sectionFor(kinds[4])).toBe('network');
        expect(sectionFor(kinds[5])).toBe('storage');
        expect(sectionFor(kinds[6])).toBe('access');
        expect(sectionFor(kinds[7])).toBe('crd');
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
        expect(m.sections.map(s => s.id)).toEqual(['cluster', 'workloads', 'network', 'config', 'storage', 'access', 'more']);
        expect(m.sections.find(s => s.id === 'workloads')!.items.map(i => i.label)).toEqual(['Pods', 'Deployments']);
    });

    it('groups custom resources by API group and hides unavailable kinds', () => {
        expect(m.crds.map(g => g.group)).toEqual(['helm.toolkit.fluxcd.io', 'kustomize.toolkit.fluxcd.io']);
        expect(m.crds.flatMap(g => g.items.map(i => i.gvr))).not.toContain('example.io/v1/hidden');
    });
});

describe('labels', () => {
    it('capitalises plurals', () => {
        expect(kindLabel(kinds[0])).toBe('Pods');
        expect(labelFromGvr('batch/v1/cronjobs')).toBe('Cronjobs');
    });
});
