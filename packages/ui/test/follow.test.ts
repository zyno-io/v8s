import { describe, expect, it } from 'vitest';

import { FollowTracker, type ScrollBox } from '../src/lib/follow.ts';

const ROW = 18;

function box(scrollTop: number, clientHeight = 100, scrollHeight = 1000): ScrollBox & { scrollTop: number; scrollHeight: number } {
    return { scrollTop, clientHeight, scrollHeight };
}
function setup() {
    let t = 0;
    const clock = { advance: (ms: number) => (t += ms) };
    const ft = new FollowTracker(() => t);
    return { ft, clock };
}

describe('FollowTracker scroll', () => {
    it('ignores our own programmatic write', () => {
        const { ft, clock } = setup();
        const el = box(0);
        ft.setTop(el, 900);
        clock.advance(10);
        expect(ft.onScroll(el, true, ROW)).toBeNull();
    });

    it('pauses when the user moves up 3px', () => {
        const { ft } = setup();
        const el = box(0);
        ft.setTop(el, 900);
        el.scrollTop = 897;
        expect(ft.onScroll(el, true, ROW)).toBe('pause');
    });

    it('does not pause for a clamp that lands at the bottom after a resize or Clear', () => {
        const { ft, clock } = setup();
        const el = box(900);
        ft.setTop(el, 900);
        clock.advance(500);
        el.scrollTop = 800;
        el.scrollHeight = 900; // content shrank; the browser clamped to the new bottom
        expect(ft.onScroll(el, true, ROW)).toBeNull();
    });

    it('resumes when the user scrolls down to the bottom while paused', () => {
        const { ft } = setup();
        const el = box(500);
        ft.painted(el);
        ft.onScroll(el, false, ROW);
        el.scrollTop = 895;
        expect(ft.onScroll(el, false, ROW)).toBe('resume');
    });

    it('does not resume short of the bottom', () => {
        const { ft } = setup();
        const el = box(500);
        ft.painted(el);
        ft.onScroll(el, false, ROW);
        el.scrollTop = 700;
        expect(ft.onScroll(el, false, ROW)).toBeNull();
    });

    it('forgives growth between the paint and the scroll event', () => {
        const { ft } = setup();
        const el = box(500);
        ft.painted(el); // the user saw a 1000px layout
        ft.onScroll(el, false, ROW);
        el.scrollTop = 900; // at the bottom of what they saw
        el.scrollHeight = 1400; // the stream grew before the event
        expect(ft.onScroll(el, false, ROW)).toBe('resume');
    });

    it('does not forgive a stale gap once the paint has caught up', () => {
        const { ft } = setup();
        const el = box(500);
        ft.painted(el);
        ft.onScroll(el, false, ROW);
        el.scrollHeight = 1400;
        ft.painted(el); // next frame
        el.scrollTop = 900;
        expect(ft.onScroll(el, false, ROW)).toBeNull();
    });
});

describe('FollowTracker wheel', () => {
    it('up at the top does nothing', () => {
        const { ft } = setup();
        expect(ft.onWheel(box(0), 0, -40, true, ROW)).toBeNull();
    });
    it('up while following pauses', () => {
        const { ft } = setup();
        expect(ft.onWheel(box(900), 0, -40, true, ROW)).toBe('pause');
    });
    it('a mostly horizontal swipe is ignored', () => {
        const { ft } = setup();
        expect(ft.onWheel(box(900), 60, -10, true, ROW)).toBeNull();
    });
    it('down at the bottom while paused resumes; not at the bottom does not', () => {
        const { ft } = setup();
        const el = box(900);
        ft.painted(el);
        expect(ft.onWheel(el, 0, 40, false, ROW)).toBe('resume');
        expect(ft.onWheel(box(300), 0, 40, false, ROW)).toBeNull();
    });
});

describe('FollowTracker keys', () => {
    it('Up while following pauses; End resumes', () => {
        const { ft } = setup();
        expect(ft.onKey(box(900), 'ArrowUp', false, true, ROW)).toBe('pause');
        expect(ft.onKey(box(900), 'PageUp', false, true, ROW)).toBe('pause');
        expect(ft.onKey(box(900), 'Home', false, true, ROW)).toBe('pause');
        expect(ft.onKey(box(900), ' ', true, true, ROW)).toBe('pause');
        expect(ft.onKey(box(0), 'ArrowUp', false, true, ROW)).toBeNull();
        expect(ft.onKey(box(300), 'End', false, false, ROW)).toBe('resume');
    });
    it('Down while paused resumes only at the bottom', () => {
        const { ft } = setup();
        const el = box(900);
        ft.painted(el);
        expect(ft.onKey(el, 'ArrowDown', false, false, ROW)).toBe('resume');
        expect(ft.onKey(el, 'PageDown', false, false, ROW)).toBe('resume');
        expect(ft.onKey(box(300), 'ArrowDown', false, false, ROW)).toBeNull();
    });
});
