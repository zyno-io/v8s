import { describe, expect, it } from 'vitest';

import { layoutServiceMap } from '../src/lib/servicemap-layout.ts';
import { classifyEdge, compact, edgeLabel, edgeLabelParts, nodeEdges, sortEdges, summarize } from '../src/lib/servicemap.ts';

const e = (from: string, to: string, requests = 1, errors = 0, drops = 0) => ({ from, to, requests, errors, drops });
const ids = (...x: string[]) => x.map(id => ({ id }));

describe('edge classification', () => {
    it('drops win over errors, errors over ok', () => {
        expect(classifyEdge(e('a', 'b', 10, 0, 0))).toBe('ok');
        expect(classifyEdge(e('a', 'b', 10, 2, 0))).toBe('error');
        expect(classifyEdge(e('a', 'b', 10, 2, 1))).toBe('dropped');
        expect(classifyEdge(e('a', 'b', 0, 0, 3))).toBe('dropped');
    });
    it('labels carry danger parts only when non-zero', () => {
        expect(edgeLabelParts(e('a', 'b', 12))).toEqual([{ text: '12 req', danger: false }]);
        expect(edgeLabel(e('a', 'b', 1500, 3, 2))).toBe('1.5k req · 3 err · 2 drop');
        expect(edgeLabelParts(e('a', 'b', 1, 1, 1)).filter(p => p.danger)).toHaveLength(2);
        expect(compact(999)).toBe('999');
        expect(compact(25000)).toBe('25k');
        expect(compact(2_500_000)).toBe('2.5M');
    });
    it('sorts drops, errors, requests', () => {
        const out = sortEdges([e('a', 'b', 100), e('a', 'c', 1, 0, 5), e('a', 'd', 5, 2)]);
        expect(out.map(x => x.to)).toEqual(['c', 'd', 'b']);
        expect(nodeEdges('d', [e('a', 'd'), e('x', 'y')])).toHaveLength(1);
        expect(summarize([e('a', 'b', 4, 1, 2), e('b', 'c', 6, 0, 1)])).toEqual({ requests: 10, errors: 1, drops: 3 });
    });
});

describe('layoutServiceMap', () => {
    const graph = {
        nodes: ids('world', 'web', 'api', 'db', 'dns'),
        edges: [e('world', 'web'), e('web', 'api'), e('api', 'db'), e('web', 'dns'), e('api', 'dns')]
    };
    const layerOf = (l: ReturnType<typeof layoutServiceMap>) => Object.fromEntries(l.nodes.map(n => [n.id, n.layer]));
    it('puts callers left of callees', () => {
        const l = layoutServiceMap(graph);
        const x = Object.fromEntries(l.nodes.map(n => [n.id, n.x]));
        expect(x.world!).toBeLessThan(x.web!);
        expect(x.web!).toBeLessThan(x.api!);
        expect(x.api!).toBeLessThan(x.db!);
        expect(x.api!).toBeLessThan(x.dns!);
        expect(layerOf(l).world).toBe(0);
    });
    it('keeps node boxes apart', () => {
        const l = layoutServiceMap(graph);
        for (const a of l.nodes)
            for (const b of l.nodes) {
                if (a === b) continue;
                const overlap = a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
                expect(overlap).toBe(false);
            }
    });
    it('is deterministic', () => {
        expect(layoutServiceMap(graph)).toEqual(layoutServiceMap(structuredClone(graph)));
    });
    it('draws every edge from the caller side to the callee side, with a label', () => {
        const l = layoutServiceMap(graph);
        expect(l.edges).toHaveLength(5);
        const byId = new Map(l.nodes.map(n => [n.id, n]));
        for (const ed of l.edges) {
            const from = byId.get(ed.from)!;
            const to = byId.get(ed.to)!;
            expect(ed.points[0]![0]).toBeCloseTo(from.x + from.w);
            expect(ed.points[ed.points.length - 1]![0]).toBeCloseTo(to.x);
            expect(Number.isFinite(ed.label[0]) && Number.isFinite(ed.label[1])).toBe(true);
        }
    });
    it('survives cycles, self loops, unknown endpoints and empty graphs', () => {
        const l = layoutServiceMap({ nodes: ids('a', 'b'), edges: [e('a', 'b'), e('b', 'a'), e('a', 'a'), e('a', 'zz')] });
        expect(l.edges).toHaveLength(3);
        expect(layoutServiceMap({ nodes: [], edges: [] })).toMatchObject({ nodes: [], edges: [], width: 0, height: 0 });
    });
    it('nudges the labels of two-way edges apart', () => {
        const l = layoutServiceMap({ nodes: ids('a', 'b'), edges: [e('a', 'b'), e('b', 'a')] });
        expect(l.edges[0]!.label[1]).not.toBe(l.edges[1]!.label[1]);
    });
});
