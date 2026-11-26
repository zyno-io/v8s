import type { Router } from 'vue-router';

import { useTableStore } from '@/stores/table.ts';

import { PODS_GVR } from './route.ts';

/**
 * Opens the Pods list filtered to one node (the node drawer's "Pods" button, `o` on a node). Like the Overview's pod links it clears
 * the list's own text and status filters and keeps the shared namespace filter.
 */
export function showPodsOnNode(router: Router, node: string): Promise<unknown> {
    const table = useTableStore();
    table.clearKindFilters(PODS_GVR);
    table.of(PODS_GVR).nodes = [node];
    return router.push('/pods');
}
