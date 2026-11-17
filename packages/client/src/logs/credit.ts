import { InitialLogCredit } from '../protocol.gen.ts';

/**
 * Client side of the log stream's credit-based flow control. The agent starts with `initial` bytes of credit, deducts every frame
 * and stops sending while the balance is not positive. The client grants bytes back as the buffer drains (a received frame has
 * been appended to the arena), in chunks so the control channel stays quiet.
 */
export class CreditGate {
    /** What the agent still believes it may send (can go negative: it may overshoot by one frame). */
    outstanding: number;
    private pending = 0;
    private paused = false;

    constructor(
        initial: number = InitialLogCredit,
        /** Smallest grant worth sending. */
        private readonly threshold: number = 64 * 1024
    ) {
        this.outstanding = initial;
    }

    /** A frame of `bytes` arrived. */
    received(bytes: number): void {
        this.outstanding -= bytes;
    }

    /** `bytes` have been consumed (appended to the buffer). Returns the credit to grant now, 0 when none is due. */
    drained(bytes: number): number {
        this.pending += bytes;
        return this.take();
    }

    setPaused(p: boolean): number {
        this.paused = p;
        return p ? 0 : this.take();
    }

    private take(): number {
        if (this.paused || this.pending < this.threshold) return 0;
        const n = this.pending;
        this.pending = 0;
        this.outstanding += n;
        return n;
    }
}
