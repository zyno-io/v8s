import { describe, expect, it } from 'vitest';

import { layoutFluxGraph } from '../src/lib/flux-layout.ts';

const n = (...ids: string[]) => ids.map(id => ({ id }));
const e = (from: string, to: string, type = 'dependsOn') => ({ from, to, type });
const L = (r: ReturnType<typeof layoutFluxGraph>) => Object.fromEntries(r.nodes.map(x => [x.id, x.layer]));

describe('layoutFluxGraph', () => {
    it('lays a chain left to right with sources at layer 0', () => {
        const r = layoutFluxGraph({ nodes: n('a', 'b', 'c'), edges: [e('c', 'b'), e('b', 'a')] });
        expect(L(r)).toEqual({ a: 0, b: 1, c: 2 });
        const x = Object.fromEntries(r.nodes.map(v => [v.id, v.x]));
        expect(x.a!).toBeLessThan(x.b!);
        expect(x.b!).toBeLessThan(x.c!);
        expect(r.edges).toHaveLength(2);
        expect(r.edges[0]!.points.length).toBeGreaterThanOrEqual(2);
    });

    it('uses longest path in a diamond', () => {
        const r = layoutFluxGraph({
            nodes: n('root', 'l', 'r', 'top'),
            edges: [e('l', 'root'), e('r', 'root'), e('top', 'l'), e('top', 'r'), e('top', 'root')]
        });
        expect(L(r)).toEqual({ root: 0, l: 1, r: 1, top: 2 });
        const ys = r.nodes.filter(v => v.layer === 1).map(v => v.y);
        expect(new Set(ys).size).toBe(2);
    });

    it('breaks cycles but still draws every edge', () => {
        const r = layoutFluxGraph({ nodes: n('a', 'b', 'c'), edges: [e('a', 'b'), e('b', 'c'), e('c', 'a'), e('a', 'a')] });
        expect(r.edges).toHaveLength(4);
        expect(new Set(r.nodes.map(v => v.layer)).size).toBe(3);
        for (const ed of r.edges) for (const p of ed.points) expect(Number.isFinite(p[0] + p[1])).toBe(true);
    });

    it('stacks disconnected components without overlap', () => {
        const r = layoutFluxGraph({ nodes: n('a', 'b', 'c', 'd'), edges: [e('b', 'a'), e('d', 'c')] });
        const a = r.nodes.find(v => v.id === 'a')!;
        const c = r.nodes.find(v => v.id === 'c')!;
        expect(Math.abs(c.y - a.y)).toBeGreaterThanOrEqual(a.h);
        expect(r.height).toBeGreaterThan(0);
    });

    it('is deterministic', () => {
        const g = {
            nodes: n(...'abcdefgh'.split('')),
            edges: [e('b', 'a'), e('c', 'a'), e('d', 'b'), e('d', 'c'), e('e', 'a'), e('f', 'e'), e('g', 'f'), e('h', 'b')]
        };
        expect(layoutFluxGraph(g)).toEqual(layoutFluxGraph(g));
    });

    it('ignores edges to unknown nodes and handles empty input', () => {
        const r = layoutFluxGraph({ nodes: n('a'), edges: [e('a', 'zz'), e('q', 'a')] });
        expect(r.edges).toHaveLength(0);
        expect(layoutFluxGraph({ nodes: [], edges: [] })).toEqual({ nodes: [], edges: [], width: 0, height: 0 });
    });

    it('handles 2000 nodes quickly', () => {
        const ids = Array.from({ length: 2000 }, (_, i) => 'n' + i);
        const edges = [];
        for (let i = 1; i < 2000; i++) {
            edges.push(e(ids[i]!, ids[Math.floor(i / 3)]!));
            if (i % 7 === 0) edges.push(e(ids[i]!, ids[(i * 13) % i]!));
        }
        const t0 = performance.now();
        const r = layoutFluxGraph({ nodes: ids.map(id => ({ id })), edges });
        expect(r.nodes).toHaveLength(2000);
        expect(performance.now() - t0).toBeLessThan(1500);
    });
});
