import type { Settings } from '@v8s/client';

import { defineStore } from 'pinia';
import { computed, ref, shallowRef } from 'vue';

import type { ColumnLayout } from '@/lib/settings.ts';

import { getAgent } from '@/lib/agent.ts';
import {
    emptySettings,
    layoutFor,
    layoutPatch,
    mergePatch,
    normalizeSettings,
    protectedClusters as protectedList,
    protectedPatch,
    toggleProtected,
    toggleStar
} from '@/lib/settings.ts';

/** The agent's settings document (stars, groups, links, bookmarks, column layouts). Small UI state; never datasets. */
export const useSettingsStore = defineStore('settings', () => {
    const doc = shallowRef<Settings>(emptySettings());
    const loaded = ref(false);

    const stars = computed(() => doc.value.stars);
    const bookmarks = computed(() => doc.value.bookmarks);
    const groups = computed(() => doc.value.groups);
    const links = computed(() => doc.value.links);
    const autoSwitchWrite = computed(() => doc.value.autoSwitchWrite);
    const protectedClusters = computed(() => protectedList(doc.value));

    async function load() {
        doc.value = confirmed = normalizeSettings(await getAgent().call<Settings>('settings.get'));
        loaded.value = true;
    }

    // The last document the agent confirmed, and the patches sent or queued behind it. Patches go out one at a time, so a slow
    // response can never overwrite a newer local change, and a list-building patch (a function of the document) is computed from
    // the confirmed document when it is sent, so rapid toggles accumulate instead of replacing each other.
    let confirmed: Settings | null = null;
    let tail: Promise<unknown> = Promise.resolve();
    const pending: Record<string, unknown>[] = [];

    const withPending = (base: Settings) => pending.reduce((d, p) => normalizeSettings(mergePatch(d, p)), base);

    /**
     * Applies the patch locally at once, then confirms with the agent (queued behind earlier patches); rolls it back if the agent
     * rejects it. A function builds the patch from the latest document, at send time.
     */
    function patch(p: Record<string, unknown> | ((doc: Settings) => Record<string, unknown>)): Promise<void> {
        const entry = typeof p === 'function' ? p(doc.value) : p;
        pending.push(entry);
        confirmed ??= doc.value;
        doc.value = normalizeSettings(mergePatch(doc.value, entry));
        const run = async () => {
            const body = typeof p === 'function' ? p(confirmed ?? doc.value) : p;
            try {
                confirmed = normalizeSettings(await getAgent().call<Settings>('settings.set', { patch: body }));
            } finally {
                pending.splice(pending.indexOf(entry), 1);
                doc.value = withPending(confirmed ?? doc.value);
            }
        };
        const result = tail.then(run, run);
        tail = result.catch(() => {});
        return result;
    }

    const toggleProtect = (id: string) => patch(d => protectedPatch(toggleProtected(protectedList(d), id)));
    const isStarred = (gvr: string) => doc.value.stars.includes(gvr);
    const toggleStarred = (gvr: string) => patch(d => ({ stars: toggleStar(d.stars, gvr) }));
    const layout = (gvr: string): ColumnLayout => layoutFor(doc.value, gvr);
    const saveLayout = (gvr: string, l: ColumnLayout) => patch(layoutPatch(gvr, l));

    return {
        doc,
        loaded,
        stars,
        bookmarks,
        groups,
        links,
        autoSwitchWrite,
        protectedClusters,
        load,
        patch,
        isStarred,
        toggleStarred,
        toggleProtect,
        layout,
        saveLayout
    };
});
