import type { FluxGraphResult } from '@v8s/client';

/**
 * Pure layered (Sugiyama-style) layout for the Flux dependency graph. No dependencies, runs in a worker or inline.
 * Edge semantics: `from` waits for / reads `to`, so layer 0 holds sources (no outgoing edges) and dependants sit to the right.
 */

export const NODE_W = 190;
export const NODE_H = 44;
export const GAP_X = 72;
export const GAP_Y = 16;
export const GAP_COMP = 40;
const SWEEPS = 4;

export interface LayoutNode {
    id: string;
    x: number;
    y: number;
    w: number;
    h: number;
    layer: number;
}

export interface LayoutEdge {
    from: string;
    to: string;
    type: string;
    points: [number, number][];
}

export interface Layout {
    nodes: LayoutNode[];
    edges: LayoutEdge[];
    width: number;
    height: number;
}

type GraphIn = Pick<FluxGraphResult, 'nodes' | 'edges'> | { nodes: { id: string }[]; edges: { from: string; to: string; type: string }[] };

export function layoutFluxGraph(graph: GraphIn): Layout {
    const ids: string[] = [];
    const index = new Map<string, number>();
    for (const n of graph.nodes) {
        if (index.has(n.id)) continue;
        index.set(n.id, ids.length);
        ids.push(n.id);
    }
    const N = ids.length;

    // Valid edges (known endpoints), self loops kept for drawing only.
    const ef: number[] = [];
    const et: number[] = [];
    const etype: string[] = [];
    for (const e of graph.edges) {
        const a = index.get(e.from);
        const b = index.get(e.to);
        if (a === undefined || b === undefined) continue;
        ef.push(a);
        et.push(b);
        etype.push(e.type);
    }
    const E = ef.length;

    // Out adjacency (edge indexes) for DFS.
    const out: number[][] = Array.from({ length: N }, () => []);
    for (let i = 0; i < E; i++) if (ef[i] !== et[i]) out[ef[i]!]!.push(i);

    // Iterative DFS: edges to a node still on the stack are back edges; they are reversed in the working DAG.
    const color = new Uint8Array(N);
    const back = new Uint8Array(E);
    const stack: number[] = [];
    const ptr = new Int32Array(N);
    for (let s = 0; s < N; s++) {
        if (color[s]) continue;
        color[s] = 1;
        stack.push(s);
        while (stack.length) {
            const v = stack[stack.length - 1]!;
            const list = out[v]!;
            if (ptr[v]! < list.length) {
                const ei = list[ptr[v]!++]!;
                const t = et[ei]!;
                if (color[t] === 0) {
                    color[t] = 1;
                    stack.push(t);
                } else if (color[t] === 1) back[ei] = 1;
            } else {
                color[v] = 2;
                stack.pop();
            }
        }
    }

    // Working DAG arcs a -> b (a is placed to the right of b): original edges, reversed when they are back edges.
    const dagOut = new Int32Array(N); // number of arcs a -> *
    const dependants: number[][] = Array.from({ length: N }, () => []); // b -> a
    const nbr: number[][] = Array.from({ length: N }, () => []); // undirected neighbours
    const parent = Int32Array.from({ length: N }, (_, i) => i);
    const find = (x: number): number => {
        while (parent[x] !== x) {
            parent[x] = parent[parent[x]!]!;
            x = parent[x]!;
        }
        return x;
    };
    for (let i = 0; i < E; i++) {
        if (ef[i] === et[i]) continue;
        const a = back[i] ? et[i]! : ef[i]!;
        const b = back[i] ? ef[i]! : et[i]!;
        dagOut[a]!++;
        dependants[b]!.push(a);
        nbr[a]!.push(b);
        nbr[b]!.push(a);
        parent[find(a)] = find(b);
    }

    // Longest-path layering (Kahn from the sources).
    const layer = new Int32Array(N);
    const remaining = Int32Array.from(dagOut);
    const queue: number[] = [];
    for (let v = 0; v < N; v++) if (remaining[v] === 0) queue.push(v);
    for (let qi = 0; qi < queue.length; qi++) {
        const b = queue[qi]!;
        for (const a of dependants[b]!) {
            if (layer[a]! < layer[b]! + 1) layer[a] = layer[b]! + 1;
            if (--remaining[a]! === 0) queue.push(a);
        }
    }

    // Components in order of first node.
    const compOf = new Map<number, number[]>();
    for (let v = 0; v < N; v++) {
        const r = find(v);
        let c = compOf.get(r);
        if (!c) compOf.set(r, (c = []));
        c.push(v);
    }

    const px = new Float64Array(N);
    const py = new Float64Array(N);
    let width = 0;
    let yOff = 0;
    const compEntries = [...compOf.values()];
    compEntries.forEach((members, ci) => {
        let maxLayer = 0;
        for (const v of members) if (layer[v]! > maxLayer) maxLayer = layer[v]!;
        const layers: number[][] = Array.from({ length: maxLayer + 1 }, () => []);
        for (const v of members) layers[layer[v]!]!.push(v);
        const pos = new Float64Array(N); // normalised position within its layer
        const setPos = (l: number[]) => l.forEach((v, i) => (pos[v] = (i + 0.5) / l.length));
        layers.forEach(setPos);
        const bary = new Float64Array(N);
        const reorder = (l: number[], below: boolean) => {
            for (const v of l) {
                let sum = 0;
                let cnt = 0;
                for (const u of nbr[v]!) {
                    if (below ? layer[u]! < layer[v]! : layer[u]! > layer[v]!) {
                        sum += pos[u]!;
                        cnt++;
                    }
                }
                bary[v] = cnt ? sum / cnt : pos[v]!;
            }
            l.sort((a, b) => bary[a]! - bary[b]!); // stable: ties keep prior order
            setPos(l);
        };
        for (let s = 0; s < SWEEPS; s++) {
            for (let li = 1; li < layers.length; li++) reorder(layers[li]!, true);
            for (let li = layers.length - 2; li >= 0; li--) reorder(layers[li]!, false);
        }
        let maxLen = 0;
        for (const l of layers) if (l.length > maxLen) maxLen = l.length;
        const compH = maxLen * NODE_H + (maxLen - 1) * GAP_Y;
        layers.forEach((l, li) => {
            const colH = l.length * NODE_H + (l.length - 1) * GAP_Y;
            const top = yOff + (compH - colH) / 2;
            l.forEach((v, i) => {
                px[v] = li * (NODE_W + GAP_X);
                py[v] = top + i * (NODE_H + GAP_Y);
            });
        });
        width = Math.max(width, (maxLayer + 1) * NODE_W + maxLayer * GAP_X);
        yOff += compH + (ci < compEntries.length - 1 ? GAP_COMP : 0);
    });

    const nodes: LayoutNode[] = ids.map((id, v) => ({ id, x: px[v]!, y: py[v]!, w: NODE_W, h: NODE_H, layer: layer[v]! }));
    const edges: LayoutEdge[] = [];
    for (let i = 0; i < E; i++) {
        const a = ef[i]!;
        const b = et[i]!;
        const A = nodes[a]!;
        const B = nodes[b]!;
        const ay = A.y + A.h / 2;
        const by = B.y + B.h / 2;
        let points: [number, number][];
        if (a === b) {
            points = [
                [A.x + A.w, ay - 8],
                [A.x + A.w + 24, ay - 8],
                [A.x + A.w + 24, ay + 8],
                [A.x + A.w, ay + 8]
            ];
        } else if (A.layer > B.layer) {
            const x1 = A.x;
            const x2 = B.x + B.w;
            const mid = (x1 + x2) / 2;
            points = [
                [x1, ay],
                [mid, ay],
                [mid, by],
                [x2, by]
            ];
        } else {
            // Reversed (cycle) edge: leave from the right of `from`, enter the left of `to`.
            const x1 = A.x + A.w;
            const x2 = B.x;
            const mid = (x1 + x2) / 2;
            points = [
                [x1, ay],
                [mid, ay],
                [mid, by],
                [x2, by]
            ];
        }
        edges.push({ from: ids[a]!, to: ids[b]!, type: etype[i]!, points });
    }
    return { nodes, edges, width: N ? width + 24 : 0, height: N ? yOff : 0 };
}
