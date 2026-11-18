import { defineStore } from 'pinia';
import { computed, ref, watch } from 'vue';

import { clampSidebarWidth, readSidebarCache, sidebarPatch, sidebarPrefs, writeSidebarCache } from '@/lib/sidebar.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useUiStore } from '@/stores/ui.ts';

/** Sidebar layout: pinned (in the layout) or collapsed (an overlay on hover or click), and its width. Persisted per user in settings. */
export const useSidebarStore = defineStore('sidebar', () => {
    const settings = useSettingsStore();
    const ui = useUiStore();
    const cache = readSidebarCache();
    const prefs = computed(() => (settings.loaded ? sidebarPrefs(settings.doc.uiPrefs) : cache));
    // A width being adjusted by keyboard shows at once and is persisted shortly after.
    const draft = ref<number | null>(null);

    const pinned = computed(() => prefs.value.pinned);
    const width = computed(() => draft.value ?? prefs.value.width);
    /** The collapsed sidebar is showing as an overlay. Meaningless while pinned. */
    const open = ref(false);
    /** The overlay was opened by the toggle button, so focus should move into it. */
    const focusOnOpen = ref(false);
    function toggleOverlay() {
        focusOnOpen.value = !open.value;
        open.value = !open.value;
    }

    watch([pinned, width], ([p, w]) => {
        if (settings.loaded) writeSidebarCache({ pinned: p, width: w });
    });

    const save = (p: Parameters<typeof sidebarPatch>[0]) =>
        settings.patch(sidebarPatch(p)).catch(e => ui.toast('danger', e instanceof Error ? e.message : String(e)));

    function setPinned(v: boolean) {
        open.value = false;
        void save({ pinned: v });
    }
    const togglePinned = () => setPinned(!pinned.value);

    let timer: ReturnType<typeof setTimeout> | undefined;
    /** Commits a width at once (drag end). */
    function commitWidth(w: number) {
        clearTimeout(timer);
        const c = clampSidebarWidth(w);
        if (c === prefs.value.width) {
            draft.value = null; // unchanged: nothing to send
            return;
        }
        draft.value = c;
        void save({ width: c }).finally(() => {
            if (draft.value === c) draft.value = null;
        });
    }
    /** Adjusts a width that changes in quick succession (keyboard repeat): shown now, persisted after a short pause. */
    function nudgeWidth(w: number) {
        draft.value = clampSidebarWidth(w);
        clearTimeout(timer);
        timer = setTimeout(() => commitWidth(draft.value ?? w), 300);
    }

    return { pinned, width, open, focusOnOpen, toggleOverlay, setPinned, togglePinned, commitWidth, nudgeWidth };
});
