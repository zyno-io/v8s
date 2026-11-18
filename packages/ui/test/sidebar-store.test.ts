import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { emptySettings, mergePatch } from '../src/lib/settings.ts';

let server = emptySettings();
let failNext = false;
const call = vi.fn(async (method: string, params?: { patch?: unknown }) => {
    if (method === 'settings.get') return server;
    if (failNext) {
        failNext = false;
        throw new Error('rejected');
    }
    server = mergePatch(server, params?.patch);
    return server;
});
vi.mock('../src/lib/agent.ts', () => ({ getAgent: () => ({ call }) }));

import { useSettingsStore } from '../src/stores/settings.ts';
import { useSidebarStore } from '../src/stores/sidebar.ts';

const sets = () => call.mock.calls.filter(c => c[0] === 'settings.set');

beforeEach(async () => {
    vi.useFakeTimers();
    setActivePinia(createPinia());
    server = emptySettings();
    failNext = false;
    call.mockClear();
    await useSettingsStore().load();
});
afterEach(() => vi.useRealTimers());

describe('sidebar store', () => {
    it('defaults to pinned at 232', () => {
        const s = useSidebarStore();
        expect(s.pinned).toBe(true);
        expect(s.width).toBe(232);
    });

    it('a drag commits once, clamped', async () => {
        const s = useSidebarStore();
        s.commitWidth(9999);
        expect(s.width).toBe(420);
        await vi.runAllTimersAsync();
        expect(sets()).toHaveLength(1);
        expect(server.uiPrefs).toEqual({ sidebarWidth: 420 });
        expect(s.width).toBe(420);
    });

    it('sends nothing when the width is unchanged', async () => {
        const s = useSidebarStore();
        s.commitWidth(232);
        await vi.runAllTimersAsync();
        expect(sets()).toHaveLength(0);
        expect(s.width).toBe(232);
    });

    it('debounces keyboard nudges into one patch', async () => {
        const s = useSidebarStore();
        s.nudgeWidth(248);
        s.nudgeWidth(264);
        s.nudgeWidth(280);
        expect(s.width).toBe(280); // shown at once
        expect(sets()).toHaveLength(0);
        await vi.advanceTimersByTimeAsync(400);
        expect(sets()).toHaveLength(1);
        expect(server.uiPrefs).toEqual({ sidebarWidth: 280 });
    });

    it('pinning persists and closes the overlay', async () => {
        const s = useSidebarStore();
        s.open = true;
        s.setPinned(false);
        expect(s.open).toBe(false);
        expect(s.pinned).toBe(false);
        await vi.runAllTimersAsync();
        expect(server.uiPrefs).toEqual({ sidebarPinned: false });
    });

    it('rolls back when the agent rejects the change', async () => {
        const s = useSidebarStore();
        failNext = true;
        s.commitWidth(300);
        expect(s.width).toBe(300);
        await vi.runAllTimersAsync();
        expect(s.width).toBe(232);
    });
});
