import { computed, watch } from 'vue';

import { useClustersStore } from '@/stores/clusters.ts';
import { useTableStore } from '@/stores/table.ts';

import type { NamespaceFilters } from './remember.ts';

import {
    compactFilters,
    connectionKey,
    KEY_NAMESPACES,
    moveEntry,
    parseEntries,
    readJson,
    setKindFilter,
    touchEntry,
    writeJson
} from './remember.ts';

let installed = false;

/**
 * Remembers namespace filters per connected cluster set: when the set changes the filters saved for it are loaded (none if unseen)
 * and the set becomes the most recently used; every filter change is saved under the current set, one kind at a time so another
 * tab's filters for other kinds survive. Call once, after the clusters are loaded.
 */
export function installNamespaceMemory(): void {
    if (installed) return;
    installed = true;
    const clusters = useClustersStore();
    const table = useTableStore();
    let current = '';
    let currentIds = '';

    const ids = computed(() =>
        clusters.activeIds
            .filter(id => clusters.nameById.has(id))
            .sort()
            .join(',')
    );
    const key = computed(() =>
        connectionKey(
            clusters.activeIds.filter(id => clusters.nameById.has(id)),
            clusters.groupMap
        )
    );
    const entries = () => parseEntries(readJson(KEY_NAMESPACES));
    const saved = (k: string): NamespaceFilters | null => entries().find(e => e.key === k)?.ns ?? null;

    watch(
        [ids, key],
        ([i, k]) => {
            const prev = current;
            const sameSet = !!currentIds && i === currentIds;
            current = k;
            currentIds = i;
            if (!k) return;
            if (sameSet && prev && prev !== k) {
                // Same clusters, new name (a group was created, renamed or deleted, or links changed): keep what is on screen.
                writeJson(KEY_NAMESPACES, moveEntry(entries(), prev, k, compactFilters(table.namespaceFilters())));
                return;
            }
            table.applyNamespaceFilters(saved(k) ?? {});
            writeJson(KEY_NAMESPACES, touchEntry(entries(), k));
        },
        { immediate: true }
    );

    table.onNamespacesChange(gvr => {
        if (!current) return;
        const list = table.of(gvr).namespaces;
        const stored = saved(current)?.[gvr] ?? [];
        if (list.length === stored.length && list.every((x, i) => x === stored[i])) return;
        writeJson(KEY_NAMESPACES, setKindFilter(entries(), current, gvr, list));
    });
}
