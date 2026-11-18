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
        doc.value = normalizeSettings(await getAgent().call<Settings>('settings.get'));
        loaded.value = true;
    }

    /** Applies the patch locally at once, then confirms with the agent; rolls back if the agent rejects it. */
    async function patch(p: Record<string, unknown>): Promise<void> {
        const prev = doc.value;
        doc.value = normalizeSettings(mergePatch(prev, p));
        try {
            doc.value = normalizeSettings(await getAgent().call<Settings>('settings.set', { patch: p }));
        } catch (e) {
            doc.value = prev;
            throw e;
        }
    }

    const toggleProtect = (id: string) => patch(protectedPatch(toggleProtected(protectedList(doc.value), id)));
    const isStarred = (gvr: string) => doc.value.stars.includes(gvr);
    const toggleStarred = (gvr: string) => patch({ stars: toggleStar(doc.value.stars, gvr) });
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
