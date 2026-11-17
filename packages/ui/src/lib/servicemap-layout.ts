import type { HubbleServiceMap } from '@v8s/client';

import type { Layout, LayoutNode } from './flux-layout.ts';

import { layoutFluxGraph } from './flux-layout.ts';

export type { LayoutNode };

export interface MapEdgeLayout {
    from: string;
    to: string;
    points: [number, number][];
    /** Where the edge label sits (the middle of the curve, nudged apart from a reverse edge). */
    label: [number, number];
}

export interface MapLayout {
    nodes: LayoutNode[];
    edges: MapEdgeLayout[];
    width: number;
    height: number;
}

type GraphIn = Pick<HubbleServiceMap, 'nodes' | 'edges'> | { nodes: { id: string }[]; edges: { from: string; to: string }[] };

/**
 * Layered left-to-right layout of a service map: callers on the left, callees to the right. Pure and deterministic; it reuses the
 * Flux layered layout (which puts the *targets* of its edges on the left) with the edges reversed, then maps the curves back.
 */
/** Extra horizontal room per layer so edge labels fit between the columns. */
export const LAYER_GAP = 96;

export function layoutServiceMap(graph: GraphIn): MapLayout {
    const base: Layout = layoutFluxGraph({
        nodes: graph.nodes.map(n => ({ id: n.id })),
        edges: graph.edges.map(e => ({ from: e.to, to: e.from, type: 'calls' }))
    });
    let maxLayer = 0;
    const nodes: LayoutNode[] = base.nodes.map(n => {
        if (n.layer > maxLayer) maxLayer = n.layer;
        return { ...n, x: n.x + n.layer * LAYER_GAP };
    });
    const byId = new Map(nodes.map(n => [n.id, n]));
    const pairs = new Set(graph.edges.map(e => `${e.from}\u0000${e.to}`));
    const edges: MapEdgeLayout[] = [];
    // The base layout already dropped edges with unknown endpoints; walk its edge list (same order, reversed direction).
    for (const be of base.edges) {
        const from = be.to;
        const to = be.from;
        const A = byId.get(from)!;
        const B = byId.get(to)!;
        const ay = A.y + A.h / 2;
        const by = B.y + B.h / 2;
        let points: [number, number][];
        if (from === to) {
            points = [
                [A.x + A.w, ay - 8],
                [A.x + A.w + 24, ay - 8],
                [A.x + A.w + 24, ay + 8],
                [A.x + A.w, ay + 8]
            ];
        } else if (B.x > A.x + A.w) {
            const mid = (A.x + A.w + B.x) / 2;
            points = [
                [A.x + A.w, ay],
                [mid, ay],
                [mid, by],
                [B.x, by]
            ];
        } else if (B.x + B.w < A.x) {
            // Cycle edge drawn back to the left.
            const mid = (A.x + B.x + B.w) / 2;
            points = [
                [A.x, ay],
                [mid, ay],
                [mid, by],
                [B.x + B.w, by]
            ];
        } else {
            // Same column: loop out of the right side.
            const bulge = Math.max(A.x + A.w, B.x + B.w) + 36;
            points = [
                [A.x + A.w, ay],
                [bulge, ay],
                [bulge, by],
                [B.x + B.w, by]
            ];
        }
        const mid = curveMid(points);
        const twoWay = from !== to && pairs.has(`${to}\u0000${from}`);
        const dy = twoWay ? (from < to ? -9 : 9) : 0;
        edges.push({ from, to, points, label: [mid[0], mid[1] + dy] });
    }
    return { nodes, edges, width: base.width + maxLayer * LAYER_GAP, height: base.height };
}

/** Point at t = 0.5 of the cubic through four points (the layout emits four), else the middle vertex. */
export function curveMid(p: [number, number][]): [number, number] {
    if (p.length === 4) {
        return [(p[0]![0] + 3 * p[1]![0] + 3 * p[2]![0] + p[3]![0]) / 8, (p[0]![1] + 3 * p[1]![1] + 3 * p[2]![1] + p[3]![1]) / 8];
    }
    const m = p[Math.floor(p.length / 2)] ?? [0, 0];
    return [m[0], m[1]];
}
