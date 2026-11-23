import type { ShallowRef } from 'vue';

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { shallowRef } from 'vue';

import { dismissedUpdate, dismissPatch, pendingUpdate, updateStatus, versionTitle, watchUpdates } from '../src/lib/update.ts';

describe('update helpers', () => {
    it('versionTitle', () => {
        expect(versionTitle('0.4.0')).toBe('v8s 0.4.0');
        expect(versionTitle('v0.4.0')).toBe('v8s 0.4.0');
        expect(versionTitle('')).toBe('v8s');
        expect(versionTitle(undefined)).toBe('v8s');
    });

    it('dismissedUpdate reads only strings', () => {
        expect(dismissedUpdate({ updateDismissed: '0.5.0' })).toBe('0.5.0');
        expect(dismissedUpdate({ updateDismissed: 5 })).toBe('');
        expect(dismissedUpdate({})).toBe('');
        expect(dismissedUpdate(null)).toBe('');
    });

    it('dismissPatch', () => {
        expect(dismissPatch('0.5.0')).toEqual({ uiPrefs: { updateDismissed: '0.5.0' } });
    });

    it('pendingUpdate hides only the dismissed version', () => {
        const s = { latest: '0.5.0', url: 'https://github.com/zyno-io/v8s/releases/tag/v0.5.0' };
        expect(pendingUpdate(s, '')).toBe(s);
        expect(pendingUpdate(s, '0.4.9')).toBe(s);
        expect(pendingUpdate(s, '0.5.0')).toBeNull();
        expect(pendingUpdate({}, '')).toBeNull();
        expect(pendingUpdate(null, '')).toBeNull();
    });
});

describe('watchUpdates', () => {
    let answers: unknown[];
    let call: ReturnType<typeof vi.fn>;
    let reconnects: ShallowRef<number>;
    let stop: () => void;
    const agent = () => ({ call, reconnects }) as unknown as Parameters<typeof watchUpdates>[0];
    const tick = () => vi.advanceTimersByTimeAsync(0);

    beforeEach(() => {
        vi.useFakeTimers();
        updateStatus.value = null;
        answers = [];
        reconnects = shallowRef(0);
        call = vi.fn(async () => {
            const a = answers.shift();
            if (a instanceof Error) throw a;
            return a;
        });
    });
    afterEach(() => {
        stop?.();
        vi.useRealTimers();
    });

    it('asks on start, on a reconnect and every 5 minutes', async () => {
        answers = [{}, { latest: '0.5.0', url: 'u' }, {}];
        stop = watchUpdates(agent());
        await tick();
        expect(call).toHaveBeenCalledTimes(1);
        expect(call).toHaveBeenCalledWith('update.status');
        expect(updateStatus.value).toBeNull();
        reconnects.value++;
        await tick();
        expect(call).toHaveBeenCalledTimes(2);
        expect(updateStatus.value).toEqual({ latest: '0.5.0', url: 'u' });
        await vi.advanceTimersByTimeAsync(5 * 60 * 1000);
        expect(call).toHaveBeenCalledTimes(3);
        expect(updateStatus.value).toBeNull();
    });

    it('does not replace the ref for an equal answer, and treats a missing result as empty', async () => {
        answers = [{ latest: '0.5.0', url: 'u' }, { latest: '0.5.0', url: 'u' }, undefined];
        stop = watchUpdates(agent());
        await tick();
        const first = updateStatus.value;
        expect(first).not.toBeNull();
        await vi.advanceTimersByTimeAsync(5 * 60 * 1000);
        expect(updateStatus.value).toBe(first);
        await vi.advanceTimersByTimeAsync(5 * 60 * 1000);
        expect(updateStatus.value).toBeNull();
    });

    it('survives a rejected call', async () => {
        answers = [new Error('unavailable'), { latest: '0.6.0', url: 'u' }];
        stop = watchUpdates(agent());
        await tick();
        expect(updateStatus.value).toBeNull();
        await vi.advanceTimersByTimeAsync(5 * 60 * 1000);
        expect(updateStatus.value?.latest).toBe('0.6.0');
    });

    it('stops polling', async () => {
        stop = watchUpdates(agent());
        await tick();
        stop();
        reconnects.value++;
        await vi.advanceTimersByTimeAsync(2 * 5 * 60 * 1000);
        expect(call).toHaveBeenCalledTimes(1);
    });
});
