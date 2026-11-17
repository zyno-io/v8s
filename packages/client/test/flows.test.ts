import { describe, expect, it } from 'vitest';

import type { Flow } from '../src/protocol.gen.ts';
import type { FlowUpdate } from '../src/types.ts';

import { FlowRing, FlowSession } from '../src/flows/session.ts';

const flow = (n: number, verdict = 'FORWARDED'): Flow => ({ time: n, verdict, src: { workload: 'a' }, dst: { workload: 'b' } });

function harness(capacity = 5) {
    const posts: FlowUpdate[] = [];
    const sent: string[] = [];
    let t = 0;
    const timers = new Map<number, () => void>();
    let id = 0;
    const s = new FlowSession(
        { sid: 1, capacity },
        {
            send: x => sent.push(x),
            closeSocket: () => {},
            post: m => posts.push(m),
            schedule: fn => {
                timers.set(++id, fn);
                return id;
            },
            cancel: h => timers.delete(h as number),
            now: () => t
        }
    );
    return {
        s,
        posts,
        sent,
        frame: (flows: Flow[], extra: object = {}) => s.onText(JSON.stringify({ flows, ...extra })),
        tick(ms = 20) {
            t += ms;
            for (const [k, fn] of Array.from(timers)) {
                timers.delete(k);
                fn();
            }
        }
    };
}

describe('FlowRing', () => {
    it('overwrites the oldest and keeps absolute numbering', () => {
        const r = new FlowRing(3);
        for (let i = 0; i < 5; i++) r.push(flow(i));
        expect(r.total).toBe(5);
        expect(r.first).toBe(2);
        expect(r.at(1)).toBeUndefined();
        expect(r.at(2)?.time).toBe(2);
        expect(r.at(4)?.time).toBe(4);
        expect(r.at(5)).toBeUndefined();
    });
});

describe('FlowSession', () => {
    it('posts the newest window when following, at most once per tick', () => {
        const h = harness(100);
        h.s.setView(0, 3, true);
        h.tick();
        h.posts.length = 0;
        h.frame([flow(0), flow(1)]);
        h.frame([flow(2), flow(3), flow(4)]);
        h.tick();
        expect(h.posts).toHaveLength(1);
        expect(h.posts[0].total).toBe(5);
        expect(h.posts[0].from).toBe(2);
        expect(h.posts[0].flows?.map(f => f.time)).toEqual([2, 3, 4]);
    });

    it('drops the oldest beyond capacity and reports first', () => {
        const h = harness(5);
        h.s.setView(0, 10, true);
        h.frame(Array.from({ length: 8 }, (_, i) => flow(i)));
        h.tick();
        const p = h.posts.at(-1)!;
        expect(p.total).toBe(8);
        expect(p.first).toBe(3);
        expect(p.flows?.map(f => f.time)).toEqual([3, 4, 5, 6, 7]);
    });

    it('carries the sampling rate when it changes', () => {
        const h = harness();
        h.s.setView(0, 5, true);
        h.frame([flow(0)], { rate: { shown: 200, total: 900, sampled: true } });
        h.tick();
        expect(h.posts.at(-1)?.rate).toEqual({ shown: 200, total: 900, sampled: true });
        h.frame([flow(1)]);
        h.tick();
        expect(h.posts.at(-1)?.rate).toBeUndefined();
    });

    it('clear() hides everything received so far', () => {
        const h = harness(10);
        h.s.setView(0, 10, true);
        h.frame([flow(0), flow(1)]);
        h.s.clear();
        h.tick();
        const p = h.posts.at(-1)!;
        expect(p.first).toBe(2);
        expect(p.flows).toEqual([]);
        h.frame([flow(2)]);
        h.tick();
        expect(h.posts.at(-1)?.flows?.map(f => f.time)).toEqual([2]);
    });

    it('sends pause/resume once per change and ends on an end frame', () => {
        const h = harness();
        h.s.setPaused(true);
        h.s.setPaused(true);
        h.s.setPaused(false);
        expect(h.sent.map(x => JSON.parse(x))).toEqual([{ pause: true }, { resume: true }]);
        h.s.onText(JSON.stringify({ end: true, error: 'relay gone' }));
        h.tick();
        expect(h.posts.at(-1)?.state).toBe('error');
        expect(h.posts.at(-1)?.error).toBe('relay gone');
    });
});
