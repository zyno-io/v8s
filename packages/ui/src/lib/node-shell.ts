import { shallowRef } from 'vue';

import { checkAccess } from '@/lib/access.ts';
import { ask } from '@/lib/ask.ts';
import { runAs } from '@/lib/mutations.ts';
import { isPhone } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useSettingsStore } from '@/stores/settings.ts';

export interface NodeShellRequest {
    cluster: string;
    node: string;
}

export const nodeShellRequest = shallowRef<NodeShellRequest | null>(null);

/** Node shell is desktop/tablet only. */
export const nodeShellAvailable = (): boolean => !isPhone.value;

export const DEFAULT_NODE_SHELL_NAMESPACE = 'v8s-system';

/** The configured node-shell namespace: the cluster's own entry, else the `*` default, else `v8s-system`. */
export function nodeShellNamespace(cluster: string): string {
    const cfg = useSettingsStore().doc.nodeShell ?? {};
    return cfg[cluster]?.namespace || cfg['*']?.namespace || DEFAULT_NODE_SHELL_NAMESPACE;
}

/** Confirms, then asks NodeShellHost to open the dock. */
export async function openNodeShell(cluster: string, node: string): Promise<void> {
    const clusters = useClustersStore();
    const name = clusters.displayName(cluster);
    const namespace = nodeShellNamespace(cluster);
    let viaWrite = false;
    try {
        const [a] = await checkAccess([{ cluster, verb: 'create', gvr: 'v1/pods', namespace }]);
        viaWrite = !!a?.viaWrite;
    } catch {
        // Unknown: say nothing about the write context rather than guess.
    }
    const ok = await ask({
        title: `Open a shell on node ${node} (${name})?`,
        body: `This starts a privileged pod on ${node} in cluster ${name}, in the ${namespace} namespace (hostPID, hostNetwork, privileged). It is deleted when you close the session and after at most 1 hour.`,
        confirmLabel: 'Start privileged shell',
        danger: true,
        note: viaWrite ? runAs(cluster, true) : undefined
    });
    if (ok) nodeShellRequest.value = { cluster, node };
}
