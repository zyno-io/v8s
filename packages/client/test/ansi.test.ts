import { describe, expect, it } from 'vitest';

import { BOLD, detectLevel, NO_COLOR, parseAnsi, RGB_FLAG, splitLine } from '../src/logs/ansi.ts';

describe('parseAnsi', () => {
    it('returns plain text untouched', () => {
        expect(parseAnsi('hello')).toEqual({ text: 'hello', spans: [] });
    });

    it('turns SGR runs into spans over the stripped text', () => {
        const r = parseAnsi('a\x1b[31mred\x1b[0m b\x1b[1;32mgreen\x1b[m');
        expect(r.text).toBe('ared bgreen');
        expect(r.spans).toEqual([1, 4, 1, NO_COLOR, 0, 6, 11, 2, NO_COLOR, BOLD]);
    });

    it('handles bright, 256 and truecolor', () => {
        const r = parseAnsi('\x1b[91mA\x1b[38;5;196mB\x1b[38;2;1;2;3mC\x1b[48;5;21mD');
        expect(r.text).toBe('ABCD');
        expect(r.spans[2]).toBe(9);
        expect(r.spans[7]).toBe(RGB_FLAG | (255 << 16));
        expect(r.spans[12]).toBe(RGB_FLAG | (1 << 16) | (2 << 8) | 3);
        expect(r.spans[18]).toBe(RGB_FLAG | 255);
    });

    it('strips non-SGR sequences and OSC titles', () => {
        expect(parseAnsi('\x1b[2Kcleared\x1b]0;title\x07 ok\x1b[?25h').text).toBe('cleared ok');
    });

    it('does not span unstyled text', () => {
        expect(parseAnsi('\x1b[0mplain').spans).toEqual([]);
    });
});

describe('detectLevel', () => {
    it('reads key=value, JSON and bracket levels', () => {
        expect(detectLevel('ts=1 level=error msg="x"')).toBe(3);
        expect(detectLevel('level="warn" msg=x')).toBe(2);
        expect(detectLevel('{"level":"fatal","msg":"x"}')).toBe(3);
        expect(detectLevel('{"severity":"WARNING"}')).toBe(2);
        expect(detectLevel('2026-01-01T00:00:00Z ERROR something')).toBe(3);
        expect(detectLevel('[WARN] disk')).toBe(2);
    });
    it('leaves info, debug and unstructured lines alone', () => {
        expect(detectLevel('level=info msg=x')).toBe(0);
        expect(detectLevel('{"level":"debug"}')).toBe(0);
        expect(detectLevel('this is an error in prose')).toBe(0);
    });
});

describe('splitLine', () => {
    it('splits source and timestamp', () => {
        expect(splitLine('web-1/app\t2026-11-17T10:00:00.123456789Z hi there', true)).toEqual({
            src: 'web-1/app',
            ts: '2026-11-17T10:00:00.123456789Z',
            rest: 'hi there'
        });
        expect(splitLine('2026-11-17T10:00:00Z hi', false)).toEqual({ src: undefined, ts: '2026-11-17T10:00:00Z', rest: 'hi' });
        expect(splitLine('no timestamp', false).rest).toBe('no timestamp');
        // a tab-bearing raw line of a single stream is not a source prefix
        expect(splitLine('a/b\tc', false)).toEqual({ src: undefined, rest: 'a/b\tc' });
    });
});
