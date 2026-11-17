import type { ViewHandle } from '@v8s/client';

import { POD } from '@v8s/client';
import { shallowRef } from 'vue';

/** The pods view currently on screen (set by the page); lets the command bar and menus read namespaces without owning data. */
export const activePods = shallowRef<ViewHandle | null>(null);

/** Distinct namespaces among the rows currently held, sorted. Scans at most a window of rows, on demand only. */
export function collectNamespaces(handle: ViewHandle | null, extra: readonly string[] = []): string[] {
    const set = new Set<string>(extra);
    if (handle) for (const row of handle.rows.values()) set.add(row[POD.namespace] as string);
    set.delete('');
    return [...set].sort();
}
