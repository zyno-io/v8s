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

    it('caps the bytes held with large lines, keeps the newest, and shrinks the arena', () => {
        const buf = new LogBuffer({ capacity: 50_000, maxBytes: 1024 * 1024 });
        const big = 'z'.repeat(10_000) + '\n';
        for (let i = 0; i < 500; i++) buf.append(b(big.replace('z', String(i % 10))));
        expect(buf.bytes).toBeLessThanOrEqual(1024 * 1024);
        expect(buf.held).toBeLessThan(110);
        expect(buf.total).toBe(500);
        expect(buf.text(499).startsWith('9')).toBe(true);
        expect(buf.has(buf.first)).toBe(true);
        // A burst of huge lines grows the arena; small lines afterwards let it shrink again.
        const arenaBefore = (buf as unknown as { arena: Uint8Array }).arena.length;
        for (let i = 0; i < 2000; i++) buf.append(b('s\n'));
        const arenaAfter = (buf as unknown as { arena: Uint8Array }).arena.length;
        expect(arenaAfter).toBeLessThanOrEqual(arenaBefore);
    });

    it('a single line larger than the cap is kept alone', () => {
        const buf = new LogBuffer({ capacity: 100, maxBytes: 2048 });
        buf.append(b('a\nb\n'));
        buf.append(b('q'.repeat(5000) + '\n'));
        expect(buf.held).toBe(1);
        expect(buf.text(buf.total - 1).length).toBe(5000);
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

    it('searches only visible text: not the timestamp, the pod/container prefix, or ANSI escapes', () => {
        const buf = new LogBuffer({ capacity: 100, multi: true });
        buf.append(
            b(
                [
                    'web-1/app\t2026-11-17T10:00:00.123Z hello world',
                    'web-1/app\t2026-11-17T10:00:00.123Z nothing',
                    'web-1/app\t2026-11-17T10:00:00Z \x1b[31mred\x1b[0m text',
                    'web-1/app\t2026-11-17T10:00:00+02:00 he\x1b[1mllo\x1b[0m again',
                    ''
                ].join('\n')
            )
        );
        const q = (s: string, mc = false) => buf.search(encodeQuery(s), mc);
        expect(q('web-1')).toEqual([]);
        expect(q('app')).toEqual([]);
        expect(q('2026')).toEqual([]);
        expect(q('10:00')).toEqual([]);
        expect(q('hello')).toEqual([0, 3]);
        expect(q('HELLO', true)).toEqual([]);
        expect(q('31m')).toEqual([]);
        expect(q('red text')).toEqual([2]);
        expect(q('hello world')).toEqual([0]);
    });

    it('a hit in the prefix does not hide a later hit in the same line', () => {
        const buf = new LogBuffer({ capacity: 100 });
        buf.append(b('2026-11-17T10:00:00Z 2026 and 2026\n'));
        expect(buf.search(encodeQuery('2026'), false)).toEqual([0]);
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
