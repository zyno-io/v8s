import { describe, expect, it } from 'vitest';

import { encodeQuery, LogBuffer } from '../src/logs/buffer.ts';

const enc = new TextEncoder();
const b = (s: string) => enc.encode(s);

describe('LogBuffer', () => {
    it('splits lines, strips CR, carries a partial line across chunks', () => {
        const buf = new LogBuffer({ capacity: 100 });
        expect(buf.append(b('one\r\ntwo\nthr'))).toBe(2);
        expect(buf.append(b('ee\nfour\n'))).toBe(2);
        expect([0, 1, 2, 3].map(i => buf.text(i))).toEqual(['one', 'two', 'three', 'four']);
        expect(buf.total).toBe(4);
        expect(buf.first).toBe(0);
    });

    it('keeps empty lines and multi-byte text', () => {
        const buf = new LogBuffer({ capacity: 10 });
        buf.append(b('a\n\nhéllo ✓\n'));
        expect(buf.text(1)).toBe('');
        expect(buf.text(2)).toBe('héllo ✓');
    });

    it('drops the oldest lines past capacity + slack, numbering stays absolute', () => {
        const buf = new LogBuffer({ capacity: 10, slack: 5 });
        for (let i = 0; i < 100; i++) buf.append(b(`line ${i}\n`));
        expect(buf.total).toBe(100);
        expect(buf.held).toBeLessThanOrEqual(15);
        expect(buf.held).toBeGreaterThanOrEqual(10);
        expect(buf.first).toBe(100 - buf.held);
        expect(buf.has(buf.first - 1)).toBe(false);
        expect(buf.text(99)).toBe('line 99');
        expect(buf.text(buf.first)).toBe(`line ${buf.first}`);
        expect(buf.dropped).toBe(buf.first);
    });

    it('compacts the arena: bytes stay bounded under a long stream', () => {
        const buf = new LogBuffer({ capacity: 1000, slack: 250 });
        const line = 'x'.repeat(99) + '\n';
        for (let i = 0; i < 50_000; i++) buf.append(b(line));
        expect(buf.bytes).toBeLessThan(1250 * 101 + 1);
        expect(buf.held).toBeLessThanOrEqual(1250);
    });

    it('grows the arena beyond its initial size', () => {
        const buf = new LogBuffer({ capacity: 100_000 });
        const big = 'y'.repeat(1000) + '\n';
        for (let i = 0; i < 500; i++) buf.append(b(big));
        expect(buf.held).toBe(500);
        expect(buf.text(499)).toBe('y'.repeat(1000));
    });

    it('searches case-insensitively by default, one hit per line, never across lines', () => {
        const buf = new LogBuffer({ capacity: 100 });
        buf.append(b('Error: boom\nok\nan ERROR and another error\nerr\nor\n'));
        expect(buf.search(encodeQuery('error'), false)).toEqual([0, 2]);
        expect(buf.search(encodeQuery('error'), true)).toEqual([2]);
        // "err" + "or" are on different lines
        expect(buf.search(encodeQuery('err\nor'), false)).toEqual([]);
        expect(buf.search(encodeQuery('zzz'), false)).toEqual([]);
    });

    it('searches from a line and after drops', () => {
        const buf = new LogBuffer({ capacity: 10, slack: 5 });
        for (let i = 0; i < 40; i++) buf.append(b(i % 2 ? 'odd\n' : 'even\n'));
        const hits = buf.search(encodeQuery('odd'), false, 30);
        expect(hits.every(h => h >= 30 && h % 2 === 1)).toBe(true);
        expect(hits.length).toBe(5);
    });

    it('clear keeps absolute numbering monotonic', () => {
        const buf = new LogBuffer({ capacity: 10 });
        buf.append(b('a\nb\n'));
        buf.clear();
        expect(buf.held).toBe(0);
        buf.append(b('c\n'));
        expect(buf.first).toBe(2);
        expect(buf.text(2)).toBe('c');
    });
});
