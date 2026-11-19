import { computed, watch } from 'vue';

import { useClustersStore } from '@/stores/clusters.ts';
import { useTableStore } from '@/stores/table.ts';

import type { NamespaceFilters } from './remember.ts';

import { compactFilters, connectionKey, KEY_NAMESPACES, parseEntries, readJson, sameFilters, upsertEntry, writeJson } from './remember.ts';

let installed = false;

/**
 * Remembers namespace filters per connected cluster set: when the set changes the filters saved for it are loaded (none if unseen),
 * and every filter change is saved under the current set. Call once, after the clusters are loaded.
 */
export function installNamespaceMemory(): void {
    if (installed) return;
    installed = true;
    const clusters = useClustersStore();
    const table = useTableStore();
    let current = '';

    const key = computed(() =>
        connectionKey(
            clusters.activeIds.filter(id => clusters.nameById.has(id)),
            clusters.groupMap
        )
    );
    const saved = (k: string): NamespaceFilters | null => parseEntries(readJson(KEY_NAMESPACES)).find(e => e.key === k)?.ns ?? null;

    watch(
        key,
        k => {
            current = k;
            if (k) table.applyNamespaceFilters(saved(k) ?? {});
        },
        { immediate: true }
    );

    table.onNamespacesChange(() => {
        if (!current) return;
        const now = compactFilters(table.namespaceFilters());
        if (sameFilters(now, saved(current))) return;
        writeJson(KEY_NAMESPACES, upsertEntry(parseEntries(readJson(KEY_NAMESPACES)), current, now));
    });
}
