import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { emptySettings, mergePatch } from '../src/lib/settings.ts';

// A fake agent: applies each patch after a delay that shrinks with every call, so a later request would finish before an earlier one
// if the store sent them concurrently.
let server = emptySettings();
let calls = 0;
const call = vi.fn(async (method: string, params?: { patch?: unknown }) => {
    if (method === 'settings.get') return server;
    const n = calls++;
    await new Promise(r => setTimeout(r, Math.max(1, 12 - n * 4)));
    if ((params?.patch as { fail?: boolean } | undefined)?.fail) throw new Error('rejected');
    server = mergePatch(server, params?.patch);
    return server;
});
vi.mock('../src/lib/agent.ts', () => ({ getAgent: () => ({ call }) }));

import { useSettingsStore } from '../src/stores/settings.ts';

beforeEach(async () => {
    setActivePinia(createPinia());
    server = emptySettings();
    calls = 0;
    call.mockClear();
});

describe('serialized settings patches', () => {
    it('rapid protect toggles all land, whatever the response timing', async () => {
        const s = useSettingsStore();
        await s.load();
        const all = [s.toggleProtect('a'), s.toggleProtect('b'), s.toggleProtect('c')];
        // Local state reflects every click at once.
        expect(s.protectedClusters).toEqual(['a', 'b', 'c']);
        await Promise.all(all);
        expect(s.protectedClusters).toEqual(['a', 'b', 'c']);
        expect(server.protectedClusters).toEqual(['a', 'b', 'c']);
        // One request at a time, in click order.
        expect(call.mock.calls.filter(c => c[0] === 'settings.set')).toHaveLength(3);
    });

    it('toggling the same cluster twice ends unprotected', async () => {
        const s = useSettingsStore();
        await s.load();
        await Promise.all([s.toggleProtect('a'), s.toggleProtect('b'), s.toggleProtect('a'), s.toggleProtect('c')]);
        expect(server.protectedClusters).toEqual(['b', 'c']);
        expect(s.protectedClusters).toEqual(['b', 'c']);
    });

    it('a rejected patch rolls back only itself', async () => {
        const s = useSettingsStore();
        await s.load();
        const ok = s.toggleProtect('a');
        const bad = s.patch({ fail: true, autoSwitchWrite: false });
        const ok2 = s.toggleStarred('apps/v1/deployments');
        await expect(bad).rejects.toThrow('rejected');
        await Promise.all([ok, ok2]);
        expect(s.protectedClusters).toEqual(['a']);
        expect(s.stars).toEqual(['apps/v1/deployments']);
        expect(s.autoSwitchWrite).toBe(true);
    });
});
