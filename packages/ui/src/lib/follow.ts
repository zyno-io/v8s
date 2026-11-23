import { BOTTOM_TOLERANCE, shouldPauseFollow } from './logs.ts';

/** The slice of an element the tracker reads (a real scroller, or a fake in tests). */
export interface ScrollBox {
    scrollTop: number;
    readonly clientHeight: number;
    readonly scrollHeight: number;
}
export type FollowChange = 'pause' | 'resume' | null;

/** A scroll event within this long of our own write, landing where we wrote, is ours. */
const MINE_MS = 150;

/**
 * Follow-the-tail state for a streaming scroller (logs, flows): tells programmatic scrolls from the user's, and decides when the
 * user's scrolling pauses or resumes following. Pure bookkeeping, no listeners; the component forwards scroll / wheel / key events.
 */
export class FollowTracker {
    private lastTop = 0;
    private expect = -1;
    private expectAt = 0;
    private paintedH = 0;

    constructor(private readonly now: () => number = () => performance.now()) {}

    /** Programmatic write; the resulting scroll event must not be mistaken for the user. */
    setTop(el: ScrollBox, top: number): void {
        el.scrollTop = top;
        this.expect = el.scrollTop;
        this.expectAt = this.now();
        this.lastTop = this.expect;
    }

    /** Once per frame: the content height the user last saw. */
    painted(el: ScrollBox): void {
        this.paintedH = el.scrollHeight;
    }

    /**
     * At the bottom of the layout the user last saw: growth between their scroll and its event does not move the target, and a
     * stale gap is not forgiven because the height is refreshed every frame.
     */
    private atBottom(el: ScrollBox, rowH: number): boolean {
        const h = this.paintedH > 0 ? Math.min(el.scrollHeight, this.paintedH) : el.scrollHeight;
        return h - (el.scrollTop + el.clientHeight) <= Math.max(BOTTOM_TOLERANCE, rowH);
    }

    onScroll(el: ScrollBox, follow: boolean, rowH: number): FollowChange {
        const top = el.scrollTop;
        const mine = this.now() - this.expectAt < MINE_MS && Math.abs(top - this.expect) < 1;
        let change: FollowChange = null;
        if (!mine) {
            if (follow) {
                if (shouldPauseFollow(this.lastTop, top, el.clientHeight, el.scrollHeight)) change = 'pause';
            } else if (top > this.lastTop && this.atBottom(el, rowH)) change = 'resume';
        }
        this.lastTop = top;
        return change;
    }

    onWheel(el: ScrollBox, dx: number, dy: number, follow: boolean, rowH: number): FollowChange {
        if (follow) return dy < 0 && Math.abs(dy) > Math.abs(dx) && el.scrollTop > 0 ? 'pause' : null;
        // No scroll event fires once already at the maximum, so the wheel has to resume.
        return dy > 0 && this.atBottom(el, rowH) ? 'resume' : null;
    }

    onKey(el: ScrollBox, key: string, shift: boolean, follow: boolean, rowH: number): FollowChange {
        if (key === 'End') return 'resume';
        if (follow) {
            const up = key === 'ArrowUp' || key === 'PageUp' || key === 'Home' || (key === ' ' && shift);
            return up && el.scrollTop > 0 ? 'pause' : null;
        }
        const down = key === 'ArrowDown' || key === 'PageDown' || (key === ' ' && !shift);
        return down && this.atBottom(el, rowH) ? 'resume' : null;
    }

    reset(): void {
        this.lastTop = 0;
        this.expect = -1;
        this.expectAt = 0;
        this.paintedH = 0;
    }
}
