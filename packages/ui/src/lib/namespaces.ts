import type { ViewHandle } from '@v8s/client';

/**
 * Distinct names held by a view's window, sorted: the `v1/namespaces` view, or the node filter's `v1/nodes` view. Reads the window once
 * per call; never per keystroke.
 */
export function namespaceNames(handle: ViewHandle | null, extra: readonly string[] = []): string[] {
    const set = new Set<string>(extra);
    const cols = handle?.columns.value;
    const idx = cols ? cols.findIndex(c => c.key === 'name') + 2 : 0;
    if (handle && idx >= 2) for (const row of handle.rows.values()) set.add(row[idx] as string);
    set.delete('');
    return [...set].sort();
}
