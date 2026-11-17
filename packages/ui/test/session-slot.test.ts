import { describe, expect, it, vi } from 'vitest';

import { createSessionSlot } from '../src/lib/session-slot.ts';

const fake = () => ({ onData: (() => {}) as ((c: Uint8Array, e: boolean) => void) | null, close: vi.fn() });
const deferred = <T>() => {
    let resolve!: (v: T) => void;
    let reject!: (e: unknown) => void;
    const promise = new Promise<T>((res, rej) => ((resolve = res), (reject = rej)));
    return { promise, resolve, reject };
};

describe('createSessionSlot', () => {
    it('two rapid opens bind only the second; the first is closed when it resolves', async () => {
        const slot = createSessionSlot<ReturnType<typeof fake>>();
        const a = deferred<ReturnType<typeof fake>>();
        const b = deferred<ReturnType<typeof fake>>();
        const first = slot.open(() => a.promise);
        const second = slot.open(() => b.promise);
        const ha = fake();
        const hb = fake();
        b.resolve(hb);
        await second;
        expect(slot.handle.value).toBe(hb);
        a.resolve(ha);
        await first;
        expect(slot.handle.value).toBe(hb);
        expect(ha.close).toHaveBeenCalled();
        expect(ha.onData).toBeNull();
        expect(hb.close).not.toHaveBeenCalled();
        expect(slot.opening.value).toBe(false);
    });

    it('a superseded open that fails is silent; the current failure rejects', async () => {
        const slot = createSessionSlot<ReturnType<typeof fake>>();
        const a = deferred<ReturnType<typeof fake>>();
        const first = slot.open(() => a.promise);
        const b = deferred<ReturnType<typeof fake>>();
        const second = slot.open(() => b.promise);
        a.reject(new Error('stale'));
        await expect(first).resolves.toBeUndefined();
        b.reject(new Error('current'));
        await expect(second).rejects.toThrow('current');
    });

    it('release closes the bound session and drops a pending one', async () => {
        const slot = createSessionSlot<ReturnType<typeof fake>>();
        const h = fake();
        await slot.open(async () => h);
        slot.release();
        expect(h.close).toHaveBeenCalled();
        expect(slot.handle.value).toBeNull();
    });
});
