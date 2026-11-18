import type { Kind, PolicyResult } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import {
    canSetMode,
    defaultPolicyKind,
    exceptionPolicyGvr,
    isLegacyPolicyGvr,
    modeTag,
    policyKinds,
    policyReportGvr,
    setModeConfirm,
    violationsOf
} from '../src/lib/policy.ts';
import { securityDecor } from '../src/lib/security-decor.ts';

const kind = (group: string, version: string, k: string, plural: string, available = true): Kind => ({
    gvr: `${group}/${version}/${plural}`,
    group,
    version,
    kind: k,
    plural,
    shortNames: [],
    namespaced: false,
    available,
    curated: true,
    clusters: ['a']
});

const ks = [
    kind('kyverno.io', 'v1', 'ClusterPolicy', 'clusterpolicies'),
    kind('policies.kyverno.io', 'v1', 'MutatingPolicy', 'mutatingpolicies'),
    kind('policies.kyverno.io', 'v1', 'ValidatingPolicy', 'validatingpolicies'),
    kind('policies.kyverno.io', 'v1', 'GeneratingPolicy', 'generatingpolicies', false),
    kind('apps', 'v1', 'Deployment', 'deployments'),
    kind('wgpolicyk8s.io', 'v1alpha2', 'PolicyReport', 'policyreports')
];

describe('policy kinds', () => {
    it('lists Kyverno policy groups with ValidatingPolicy first and legacy last', () => {
        const l = policyKinds(ks);
        expect(l.map(k => k.kind)).toEqual(['ValidatingPolicy', 'MutatingPolicy', 'ClusterPolicy']);
        expect(defaultPolicyKind(l)).toBe('policies.kyverno.io/v1/validatingpolicies');
        expect(defaultPolicyKind([ks[0]])).toBe('kyverno.io/v1/clusterpolicies');
        expect(defaultPolicyKind([])).toBe('');
    });
    it('knows legacy and settable kinds', () => {
        expect(isLegacyPolicyGvr('kyverno.io/v1/clusterpolicies')).toBe(true);
        expect(isLegacyPolicyGvr('kyverno.io/v2/policyexceptions')).toBe(false);
        expect(canSetMode('policies.kyverno.io/v1/validatingpolicies')).toBe(true);
        expect(canSetMode('policies.kyverno.io/v1/imagevalidatingpolicies')).toBe(true);
        expect(canSetMode('policies.kyverno.io/v1/mutatingpolicies')).toBe(false);
        expect(canSetMode('kyverno.io/v1/clusterpolicies')).toBe(false);
    });
    it('finds report and exception kinds', () => {
        expect(policyReportGvr(ks)).toBe('wgpolicyk8s.io/v1alpha2/policyreports');
        expect(policyReportGvr(ks, true)).toBe('');
        expect(exceptionPolicyGvr(ks)).toBe('policies.kyverno.io/v1/validatingpolicies');
        expect(exceptionPolicyGvr([ks[0]])).toBe('kyverno.io/v1/clusterpolicies');
    });
});

describe('mode tags', () => {
    it('maps modes to tones', () => {
        expect(modeTag('Enforce')).toEqual({ text: 'Enforce', tone: 'danger' });
        expect(modeTag('Audit')).toEqual({ text: 'Audit', tone: 'info' });
        expect(modeTag('Mixed')).toEqual({ text: 'Mixed', tone: 'warning' });
        expect(modeTag('')).toBeNull();
    });
    it('decorates policy tables and report counts', () => {
        const col = (key: string, idx: number) =>
            ({ key, label: key, idx, kind: 'text', align: 'left', width: 80, grow: false, sortable: true }) as never;
        const row = [1, 1, 'Enforce', 'true'] as never;
        const d = securityDecor('policies.kyverno.io/v1/validatingpolicies');
        expect(d?.cell?.(col('mode', 2), row)?.tag).toEqual({ text: 'Enforce', tone: 'danger' });
        const legacy = securityDecor('kyverno.io/v1/clusterpolicies');
        expect(legacy?.cell?.(col('readOnly', 3), [1, 1, 'Audit', 'false'] as never)?.tag?.text).toBe('Read-only');
        const trivy = securityDecor('aquasecurity.github.io/v1alpha1/vulnerabilityreports');
        expect(trivy?.cell?.(col('critical', 2), [1, 1, 4] as never)?.tag).toEqual({ text: '4', tone: 'danger' });
        expect(trivy?.cell?.(col('high', 2), [1, 1, 0] as never)?.text).toBe('0');
        expect(trivy?.cell?.(col('workload', 2), [1, 1, 'x'] as never)).toBeNull();
        expect(securityDecor('v1/pods')).toBeNull();
    });
});

describe('mode confirmation', () => {
    const base = { policy: 'require-team-label', cluster: 'prod-1', current: 'Audit', fails: 12, protected: false } as const;
    it('warns that Enforce blocks and shows the fail count', () => {
        const t = setModeConfirm({ ...base, mode: 'Enforce' });
        expect(t.danger).toBe(true);
        expect(t.confirmLabel).toBe('Enforce');
        expect(t.body).toContain('reject');
        expect(t.body).toContain('12 failing results');
        expect(t.phrase).toBeUndefined();
    });
    it('asks for the policy name on a protected cluster and says when the count is unknown or partial', () => {
        const t = setModeConfirm({ ...base, mode: 'Enforce', protected: true, fails: null });
        expect(t.phrase).toBe('require-team-label');
        expect(t.body).toContain('not known');
        expect(setModeConfirm({ ...base, mode: 'Audit', fails: 3, partial: true }).body).toContain('3+ failing results');
        expect(setModeConfirm({ ...base, mode: 'Audit', fails: 0 }).danger).toBe(false);
        expect(setModeConfirm({ ...base, mode: 'Audit', fails: 1 }).body).toContain('1 failing result found');
    });
});

describe('violations', () => {
    const r = (policy: string, result: string): PolicyResult => ({ policy, result });
    it('keeps failing and erroring results of the policy only', () => {
        const out = violationsOf('p', [r('p', 'fail'), r('p', 'pass'), r('q', 'fail'), r('ns/p', 'error'), r('p', 'warn')]);
        expect(out.map(x => x.policy + ':' + x.result)).toEqual(['p:fail', 'ns/p:error']);
    });
});
