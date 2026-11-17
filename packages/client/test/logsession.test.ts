import { describe, expect, it } from 'vitest';

import type { LogUpdate } from '../src/types.ts';

import { CreditGate } from '../src/logs/credit.ts';
import { LogSession } from '../src/logs/session.ts';

const enc = new TextEncoder();

function harness(capacity = 1000, multi = false) {
    const posts: LogUpdate[] = [];
    const sent: string[] = [];
    let t = 0;
    const timers = new Map<number, () => void>();
    let id = 0;
    const s = new LogSession(
        { sid: 1, capacity, multi },
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
        timers,
        tick(ms = 20) {
            t += ms;
            for (const [k, fn] of Array.from(timers)) {
                timers.delete(k);
                fn();
            }
        }
    };
}

describe('CreditGate', () => {
    it('starts at 256 KiB and grants in chunks as data drains', () => {
        const g = new CreditGate();
        expect(g.outstanding).toBe(256 * 1024);
        g.received(30_000);
        expect(g.drained(30_000)).toBe(0);
        g.received(40_000);
        expect(g.drained(40_000)).toBe(70_000);
        expect(g.outstanding).toBe(256 * 1024);
    });

    it('never stalls: a sender that used all credit always has enough pending to be granted', () => {
        const g = new CreditGate(1000, 400);
        let granted = 0;
        for (let i = 0; i < 20; i++) {
            g.received(100);
            granted += g.drained(100);
        }
        expect(g.outstanding).toBeGreaterThan(0);
        expect(granted).toBeGreaterThan(0);
    });

    it('withholds while paused and releases on resume', () => {
        const g = new CreditGate(1000, 100);
        g.setPaused(true);
        g.received(500);
        expect(g.drained(500)).toBe(0);
        expect(g.setPaused(false)).toBe(500);
    });
});

describe('LogSession', () => {
    it('posts at most once per frame with counters and the follow viewport', () => {
        const h = harness();
        h.s.setView(0, 5, true);
        h.tick();
        h.posts.length = 0;
        for (let i = 0; i < 50; i++) h.s.onBinary(enc.encode(`line ${i}\n`));
        expect(h.posts.length).toBe(0);
        h.tick();
        expect(h.posts.length).toBe(1);
        const p = h.posts[0];
        expect(p.total).toBe(50);
        expect(p.from).toBe(45);
        expect(p.lines!.map(l => l.text)).toEqual(['line 45', 'line 46', 'line 47', 'line 48', 'line 49']);
    });

    it('does not re-post lines when the viewport is scrolled away from new lines', () => {
        const h = harness();
        for (let i = 0; i < 100; i++) h.s.onBinary(enc.encode(`l${i}\n`));
        h.s.setView(10, 5, false);
        h.tick();
        h.posts.length = 0;
        h.s.onBinary(enc.encode('more\n'));
        h.tick();
        expect(h.posts[0].total).toBe(101);
        expect(h.posts[0].lines).toBeUndefined();
    });

    it('sends credit grants as the buffer drains', () => {
        const h = harness();
        const chunk = enc.encode('x'.repeat(40_000) + '\n');
        h.s.onBinary(chunk);
        expect(h.sent).toEqual([]);
        h.s.onBinary(chunk);
        expect(h.sent.map(x => JSON.parse(x))).toEqual([{ credit: 80_002 }]);
    });

    it('pausing withholds credit; resume grants it', () => {
        const h = harness();
        h.s.setPaused(true);
        h.s.onBinary(enc.encode('x'.repeat(100_000) + '\n'));
        expect(h.sent).toEqual([]);
        h.s.setPaused(false);
        expect(JSON.parse(h.sent[0]).credit).toBeGreaterThan(0);
    });

    it('parses ANSI, level, timestamp and source in the posted lines', () => {
        const h = harness(1000, true);
        h.s.setView(0, 10, true);
        h.s.onBinary(enc.encode('web-1/app\t2026-11-17T10:00:00Z \x1b[31mlevel=error\x1b[0m boom\n'));
        h.tick();
        const l = h.posts.at(-1)!.lines![0];
        expect(l.src).toBe('web-1/app');
        expect(l.ts).toBe('2026-11-17T10:00:00Z');
        expect(l.text).toBe('level=error boom');
        expect(l.level).toBe(3);
        expect(l.spans!.length).toBe(5);
    });

    it('searches, steps next/prev with wraparound, reveals, and picks up new lines', () => {
        const h = harness();
        h.s.setView(0, 20, false);
        h.s.onBinary(enc.encode('a\nneedle 1\nb\nc\nneedle 2\nd\n'));
        h.s.search('NEEDLE', false);
        h.tick();
        expect(h.posts.at(-1)!.search).toMatchObject({ count: 2, current: 0 });
        expect(h.posts.at(-1)!.lines![1].hl).toEqual([0, 6]);
        h.s.searchStep(1, 0);
        h.tick();
        expect(h.posts.at(-1)!.search).toMatchObject({ current: 1, reveal: 1 });
        h.s.searchStep(1);
        h.tick();
        expect(h.posts.at(-1)!.search).toMatchObject({ current: 2, reveal: 4 });
        h.s.searchStep(1);
        h.tick();
        expect(h.posts.at(-1)!.search).toMatchObject({ current: 1, reveal: 1 });
        h.s.searchStep(-1);
        h.tick();
        expect(h.posts.at(-1)!.search).toMatchObject({ current: 2, reveal: 4 });
        h.s.onBinary(enc.encode('needle 3\n'));
        h.tick();
        expect(h.posts.at(-1)!.search!.count).toBe(3);
    });

    it('reports the end of the stream', () => {
        const h = harness();
        h.s.onText('{"end":true}');
        h.tick();
        expect(h.posts.at(-1)!.state).toBe('ended');
        const h2 = harness();
        h2.s.onText('{"end":true,"error":"pod gone"}');
        h2.tick();
        expect(h2.posts.at(-1)).toMatchObject({ state: 'error', error: 'pod gone' });
    });

    it('keeps matches consistent when lines drop out of the ring', () => {
        const h = harness(10);
        h.s.search('hit', false);
        for (let i = 0; i < 1000; i++) h.s.onBinary(enc.encode(i % 10 === 0 ? 'hit\n' : 'miss\n'));
        h.tick();
        const first = h.posts.at(-1)!.first;
        expect(first).toBeGreaterThan(500);
        const count = h.posts.at(-1)!.search!.count;
        expect(count).toBeLessThanOrEqual(Math.ceil((1000 - first) / 10));
    });
});
