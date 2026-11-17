import { describe, expect, it } from 'vitest';

import { countQuery, healthKinds, integrationsOf, phaseLabel, podOther, problemsQuery } from '../src/lib/cluster-overview.ts';

describe('cluster overview queries', () => {
    it('scopes count views to one cluster and tones', () => {
        const q = countQuery('prod', 'v1/pods', [3, 2]);
        expect(q.clusters).toEqual(['prod']);
        expect(q.filters).toEqual([{ col: 'statusTone', op: 'in', value: ['3', '2'] }]);
        expect(countQuery('prod', 'v1/nodes').filters).toEqual([]);
    });

    it('problems by severity exclude acknowledged', () => {
        const q = problemsQuery('prod', 'critical');
        expect(q.gvr).toBe('v8s/v1/problems');
        expect(q.filters).toContainEqual({ col: 'acked', op: 'eq', value: 'false' });
        expect(q.filters).toContainEqual({ col: 'severity', op: 'in', value: ['critical'] });
    });

    it('detects per-cluster flux/helm and integrations from the kinds list', () => {
        const kinds = [
            { gvr: 'kustomize.toolkit.fluxcd.io/v1/kustomizations', group: 'kustomize.toolkit.fluxcd.io', clusters: ['a'] },
            { gvr: 'helm.sh/v1/releases', group: 'helm.sh', clusters: ['a', 'b'] },
            { gvr: 'aquasecurity.github.io/v1alpha1/vulnerabilityreports', group: 'aquasecurity.github.io', clusters: ['b'] }
        ];
        expect(healthKinds(kinds, 'a')).toEqual({ kustomizations: kinds[0].gvr, helmReleases: '', helmSecrets: 'helm.sh/v1/releases' });
        expect(healthKinds(kinds, 'b').kustomizations).toBe('');
        expect(integrationsOf(kinds, 'a').trivy).toBe(false);
        expect(integrationsOf(kinds, 'b').trivy).toBe(true);
    });

    it('pod buckets and phases', () => {
        expect(podOther({ total: 10, running: 6, failing: 1, pending: 1, completed: 1 })).toBe(1);
        expect(phaseLabel('auth-required')).toBe('Sign-in required');
    });
});
