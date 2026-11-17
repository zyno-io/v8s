import type { Kind, PolicyResult } from '@v8s/client';

import type { Tone } from './table-decor.ts';

// Pure logic of the Kyverno policy views.

export const KYVERNO_POLICY_GROUPS = new Set(['policies.kyverno.io', 'kyverno.io']);

/** Legacy `kyverno.io/v1` ClusterPolicy / Policy: read-only (since Kyverno 1.20 they are neither executed nor accepted for writes). */
export function isLegacyPolicyGvr(gvr: string): boolean {
    return /^kyverno\.io\/v1\/(clusterpolicies|policies)$/.test(gvr);
}

export const LEGACY_HINT = 'Legacy kyverno.io/v1 policy: read-only. Migrate it to a CEL policy (policies.kyverno.io) to change its mode.';

/** Kinds that get a tab on the Policies page: the Kyverno policy groups, available in a selected cluster. */
export function policyKinds(kinds: readonly Kind[]): Kind[] {
    const list = kinds.filter(k => KYVERNO_POLICY_GROUPS.has(k.group) && k.available);
    const rank = (k: Kind) => (k.kind === 'ValidatingPolicy' ? 0 : k.group === 'policies.kyverno.io' ? 1 : 2);
    return list.sort((a, b) => rank(a) - rank(b) || a.kind.localeCompare(b.kind));
}

/** ValidatingPolicy when served, else the first kind. */
export function defaultPolicyKind(list: readonly Kind[]): string {
    return (list.find(k => k.kind === 'ValidatingPolicy') ?? list[0])?.gvr ?? '';
}

/** `Enforce` danger, `Audit` info, `Mixed` warning; anything else (a kind without modes) shows no tag. */
export function modeTag(mode: string): { text: string; tone: Tone } | null {
    switch (mode) {
        case 'Enforce':
            return { text: 'Enforce', tone: 'danger' };
        case 'Audit':
            return { text: 'Audit', tone: 'info' };
        case 'Mixed':
            return { text: 'Mixed', tone: 'warning' };
        default:
            return null;
    }
}

/** Only CEL policies that can be switched have a Validating mode. */
export const canSetMode = (gvr: string): boolean => !isLegacyPolicyGvr(gvr) && /^policies\.kyverno\.io\/[^/]+\/[a-z]*validatingpolicies$/.test(gvr);

export interface ModeConfirmInput {
    policy: string;
    cluster: string;
    mode: 'Audit' | 'Enforce';
    current: string;
    /** Failing results seen for this policy, or null when not (yet) counted. */
    fails: number | null;
    /** True when the scan of failing reports was bounded and may have missed some. */
    partial?: boolean;
    production: boolean;
}

export interface ConfirmText {
    title: string;
    body: string;
    confirmLabel: string;
    danger: boolean;
    phrase?: string;
    note?: string;
}

export function failText(fails: number | null, partial = false): string {
    if (fails === null) return 'The current number of failing resources is not known.';
    if (fails === 0) return partial ? 'No failures found in the reports checked.' : 'No resources currently fail it.';
    return `${fails}${partial ? '+' : ''} failing result${fails === 1 ? '' : 's'} found in the reports checked.`;
}

/** Confirmation of `kyverno.setMode`: shows the current fail count; Enforce warns that it blocks admissions and, in production, needs the policy name typed. */
export function setModeConfirm(i: ModeConfirmInput): ConfirmText {
    const where = `${i.policy} on ${i.cluster}`;
    if (i.mode === 'Enforce') {
        return {
            title: `Enforce ${i.policy}?`,
            body: `${where} switches from ${i.current || 'its current mode'} to Enforce: Kubernetes will reject new and updated resources that violate it. ${failText(i.fails, i.partial)} Those resources are blocked on their next change.`,
            confirmLabel: 'Enforce',
            danger: true,
            phrase: i.production ? i.policy : undefined,
            note: i.production ? 'Production cluster: type the policy name to confirm.' : undefined
        };
    }
    return {
        title: `Switch ${i.policy} to Audit?`,
        body: `${where} switches from ${i.current || 'its current mode'} to Audit: violations are reported but no longer block admissions. ${failText(i.fails, i.partial)}`,
        confirmLabel: 'Switch to Audit',
        danger: false,
        phrase: i.production ? i.policy : undefined,
        note: i.production ? 'Production cluster: type the policy name to confirm.' : undefined
    };
}

/** Results of one policy that count as violations. A namespaced policy may be reported as `<namespace>/<name>`. */
export function violationsOf(policy: string, results: readonly PolicyResult[]): PolicyResult[] {
    return results.filter(r => (r.policy === policy || r.policy.endsWith('/' + policy)) && (r.result === 'fail' || r.result === 'error'));
}

export interface Violation {
    result: PolicyResult;
    cluster: string;
    kind: string;
    namespace: string;
    name: string;
}

/** Policy report GVRs: Kyverno's default API (wgpolicyk8s.io) first, else the opt-in openreports.io one. */
export function policyReportGvr(kinds: readonly Kind[], clusterScoped = false): string {
    const want = clusterScoped ? ['ClusterPolicyReport', 'ClusterReport'] : ['PolicyReport', 'Report'];
    for (const [group, kind] of [
        ['wgpolicyk8s.io', want[0]],
        ['openreports.io', want[1]]
    ])
        for (const k of kinds) if (k.available && k.group === group && k.kind === kind) return k.gvr;
    return '';
}

/**
 * The policy kind to name in a PolicyException request when only a policy name is known (a report result): a CEL ValidatingPolicy
 * when served, else the legacy ClusterPolicy.
 */
export function exceptionPolicyGvr(kinds: readonly Kind[]): string {
    const cel = kinds.find(k => k.available && k.group === 'policies.kyverno.io' && k.kind === 'ValidatingPolicy');
    if (cel) return cel.gvr;
    return kinds.find(k => k.available && k.group === 'kyverno.io' && k.kind === 'ClusterPolicy')?.gvr ?? '';
}
