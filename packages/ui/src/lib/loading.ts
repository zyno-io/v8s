import type { ClusterState } from '@v8s/client';

/** The agent reports this cluster as still loading `gvr` (its first list has not finished, or discovery is pending for it). */
export function kindLoading(s: Pick<ClusterState, 'phase' | 'loading'>, gvr: string): boolean {
    return s.phase !== 'unreachable' && s.phase !== 'auth-required' && s.loading?.includes(gvr) === true;
}

export type EmptyKind = 'no-clusters' | '' | 'loading' | 'syncing' | 'filtered' | 'none';

export interface EmptyInput {
    clusters: number;
    total: number;
    /** 0 until the view's first patch. */
    version: number;
    filtered: boolean;
    /** Clusters still loading this kind. */
    loading: number;
    /** Some cluster is mid elevate/revert. */
    switching: boolean;
    /** Some selected cluster is connecting or syncing. */
    syncing: boolean;
}

/** Which empty state a table with no rows shows. Anything still loading outranks "filtered" and "none": rows are about to arrive. */
export function emptyKindOf(i: EmptyInput): EmptyKind {
    if (!i.clusters) return 'no-clusters';
    if (i.total > 0) return '';
    if (i.version === 0) return 'loading';
    if (i.loading > 0 || i.switching) return 'syncing';
    if (i.filtered) return 'filtered';
    if (i.syncing) return 'syncing';
    return 'none';
}

/** "a, b and c", at most three names, then "and N more". */
export function loadingNames(ids: readonly string[], displayName: (id: string) => string): string {
    const names = ids.map(displayName);
    if (names.length <= 3) return names.length > 1 ? `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}` : (names[0] ?? '');
    return `${names.slice(0, 3).join(', ')} and ${names.length - 3} more`;
}
