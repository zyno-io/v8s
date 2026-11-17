import type { Kind } from '@v8s/client';

export interface Integrations {
    trivy: boolean;
    kyverno: boolean;
    kubescape: boolean;
    /** Any of Kyverno, Trivy, Kubescape: the Security section shows. */
    security: boolean;
}

const KYVERNO_GROUPS = new Set(['kyverno.io', 'policies.kyverno.io']);
const TRIVY_GROUP = 'aquasecurity.github.io';
const KUBESCAPE_GROUP = 'spdx.softwarecomposition.kubescape.io';

/** What the selected clusters' discovery says is installed (Hubble is probed separately: it has no kinds). */
export function detectIntegrations(kinds: readonly Pick<Kind, 'group'>[]): Integrations {
    let trivy = false;
    let kyverno = false;
    let kubescape = false;
    for (const k of kinds) {
        if (k.group === TRIVY_GROUP) trivy = true;
        else if (KYVERNO_GROUPS.has(k.group)) kyverno = true;
        else if (k.group === KUBESCAPE_GROUP) kubescape = true;
    }
    return { trivy, kyverno, kubescape, security: trivy || kyverno || kubescape };
}

/** Kinds whose drawer has a Security tab, and kinds whose drawer has a Flows tab. */
export const SECURITY_GVRS = new Set([
    'v1/pods',
    'apps/v1/deployments',
    'apps/v1/statefulsets',
    'apps/v1/daemonsets',
    'apps/v1/replicasets',
    'batch/v1/jobs',
    'batch/v1/cronjobs'
]);
export const FLOWS_GVRS = new Set([
    'v1/pods',
    'apps/v1/deployments',
    'apps/v1/statefulsets',
    'apps/v1/daemonsets',
    'apps/v1/replicasets',
    'v1/services',
    'v1/namespaces'
]);
