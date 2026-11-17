import { ref, shallowRef } from 'vue';

/** The slice of a stream handle the slot needs to detach and close it. */
export interface Closable {
    onData: ((chunk: Uint8Array, stderr: boolean) => void) | null;
    close(): void;
}

/**
 * Holds at most one interactive session. `open` always replaces whatever is bound or still opening: the sequence bump in
 * `release` makes a superseded open close its stream when it finally resolves, so a stale session can never be bound.
 */
export function createSessionSlot<H extends Closable>() {
    const handle = shallowRef<H | null>(null);
    const opening = ref(false);
    let seq = 0;

    function release() {
        seq++;
        const h = handle.value;
        handle.value = null;
        opening.value = false;
        if (h) {
            h.onData = null;
            h.close();
        }
    }

    /** Resolves once bound (or superseded; errors of a superseded open are dropped). Rejects with the current open's error. */
    async function open(factory: () => Promise<H>): Promise<void> {
        release();
        const mine = seq;
        opening.value = true;
        try {
            const h = await factory();
            if (mine !== seq) {
                h.onData = null;
                h.close();
                return;
            }
            handle.value = h;
        } catch (e) {
            if (mine === seq) throw e;
        } finally {
            if (mine === seq) opening.value = false;
        }
    }

    return { handle, opening, release, open };
}
