import type { ServiceMapEdge, ServiceMapNode } from '@v8s/client';

// Pure helpers behind the service map (no Vue, no DOM).

export type EdgeClass = 'dropped' | 'error' | 'ok';

/** Dropped traffic wins (dashed, danger); otherwise errors tint it danger; otherwise healthy. */
export function classifyEdge(e: Pick<ServiceMapEdge, 'requests' | 'errors' | 'drops'>): EdgeClass {
    if (e.drops > 0) return 'dropped';
    if (e.errors > 0) return 'error';
    return 'ok';
}

export const edgeWord = (c: EdgeClass): string => (c === 'dropped' ? 'Dropped' : c === 'error' ? 'Errors' : 'OK');

export const edgeKey = (e: Pick<ServiceMapEdge, 'from' | 'to'>): string => `${e.from}→${e.to}`;

/** Label parts: requests first, then errors and drops (each only when non-zero); `danger` marks the parts drawn in the danger colour. */
export function edgeLabelParts(e: Pick<ServiceMapEdge, 'requests' | 'errors' | 'drops'>): { text: string; danger: boolean }[] {
    const parts = [{ text: `${compact(e.requests)} req`, danger: false }];
    if (e.errors > 0) parts.push({ text: `${compact(e.errors)} err`, danger: true });
    if (e.drops > 0) parts.push({ text: `${compact(e.drops)} drop`, danger: true });
    return parts;
}
export const edgeLabel = (e: Pick<ServiceMapEdge, 'requests' | 'errors' | 'drops'>): string =>
    edgeLabelParts(e)
        .map(p => p.text)
        .join(' · ');

/** 1234 -> 1.2k, 1500000 -> 1.5M. */
export function compact(n: number): string {
    if (n < 1000) return String(n);
    if (n < 1_000_000) return `${+(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;
    return `${+(n / 1_000_000).toFixed(1)}M`;
}

/** Drops first, then errors, then requests; the key makes it deterministic. */
export function sortEdges(edges: readonly ServiceMapEdge[]): ServiceMapEdge[] {
    return [...edges].sort((a, b) => b.drops - a.drops || b.errors - a.errors || b.requests - a.requests || edgeKey(a).localeCompare(edgeKey(b)));
}

export const nodeEdges = (id: string, edges: readonly ServiceMapEdge[]): ServiceMapEdge[] =>
    sortEdges(edges.filter(e => e.from === id || e.to === id));

export function nodeLabel(n: Pick<ServiceMapNode, 'namespace' | 'workload' | 'id'>): string {
    return n.workload || n.id;
}

/** Reserved identities have no namespace and no workload object to open. */
export const isOpenable = (n: Pick<ServiceMapNode, 'namespace' | 'workload'>): boolean => !!n.namespace && !!n.workload;

export function summarize(edges: readonly ServiceMapEdge[]): { requests: number; errors: number; drops: number } {
    let requests = 0;
    let errors = 0;
    let drops = 0;
    for (const e of edges) {
        requests += e.requests;
        errors += e.errors;
        drops += e.drops;
    }
    return { requests, errors, drops };
}
