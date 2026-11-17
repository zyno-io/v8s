import { shallowRef } from 'vue';

import { ask } from '@/lib/ask.ts';
import { isPhone } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';

export interface NodeShellRequest {
    cluster: string;
    node: string;
}

export const nodeShellRequest = shallowRef<NodeShellRequest | null>(null);

/** Node shell is desktop/tablet only. */
export const nodeShellAvailable = (): boolean => !isPhone.value;

/** Confirms, then asks NodeShellHost to open the dock. */
export async function openNodeShell(cluster: string, node: string): Promise<void> {
    const wc = useClustersStore().writeContextOf(cluster);
    const ok = await ask({
        title: `Open a shell on node ${node}?`,
        body: `This starts a privileged pod on ${node} in the v8s-system namespace (hostPID, hostNetwork, privileged). It is deleted when you close the session and after at most 1 hour.`,
        confirmLabel: 'Start privileged shell',
        danger: true,
        note: wc ? `Runs as ${wc}` : undefined
    });
    if (ok) nodeShellRequest.value = { cluster, node };
}
