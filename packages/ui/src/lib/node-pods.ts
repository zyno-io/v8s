import type { Router } from 'vue-router';

import { useTableStore } from '@/stores/table.ts';

import { PODS_GVR } from './route.ts';

/**
 * Opens the Pods list showing every pod on one node (the node drawer's "Pods on this node", `o` on a node): the node filter set to it,
 * and the namespace filter and the list's own text and status filters cleared, since a node's pods span namespaces.
 */
export function showPodsOnNode(router: Router, node: string): Promise<unknown> {
    const table = useTableStore();
    table.clearKindFilters(PODS_GVR);
    table.setNamespaces([]);
    table.of(PODS_GVR).nodes = [node];
    return router.push('/pods');
}
