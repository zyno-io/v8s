import { nextTick } from 'vue';

import router from '@/router.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useSidebarStore } from '@/stores/sidebar.ts';
import { useTableStore } from '@/stores/table.ts';
import { useUiStore } from '@/stores/ui.ts';

import { closeDrawer, isDrawerOpen } from './drawer.ts';

/** A page may claim j/k/x/Enter. Returns true when it handled the key. */
type PageKeyHandler = (e: KeyboardEvent) => boolean;
let pageHandler: PageKeyHandler | null = null;

export function setPageKeyHandler(h: PageKeyHandler | null) {
    pageHandler = h;
}

/**
 * The list on screen (Resources table, Problems list) registers itself so Enter in the filter can move focus into it and
 * Esc from it can clear the filter. Only one list is on screen at a time.
 */
export interface ListNav {
    /** Focus the list and its row focus (the first row when `first`, else a still-present focused row). False when there are no rows. */
    focusList(first: boolean): boolean;
    /** True when the element is the list or inside it. */
    owns(el: EventTarget | null): boolean;
    /** The page's filter text, when it lives outside the table store. */
    filterText?(): string;
    clearFilter?(): void;
}
let listNav: ListNav | null = null;

export function setListNav(n: ListNav | null) {
    listNav = n;
}

const FILTER_ID = 'pods-filter';

function isEditable(t: EventTarget | null): boolean {
    if (!(t instanceof HTMLElement)) return false;
    if (t.isContentEditable) return true;
    const tag = t.tagName;
    if (tag === 'TEXTAREA' || tag === 'SELECT') return true;
    if (tag === 'INPUT') {
        const type = (t as HTMLInputElement).type;
        return !(type === 'checkbox' || type === 'radio' || type === 'button');
    }
    return false;
}

/** Esc order from the design system: clear filter, then selection, then close the drawer. */
export function escapeChain(): boolean {
    return escapeStep() !== null;
}

function escapeStep(): 'filter' | 'other' | null {
    if (listNav?.filterText?.()) {
        listNav.clearFilter?.();
        return 'filter';
    }
    const table = useTableStore();
    const gvr = table.currentGvr;
    if (gvr) {
        const st = table.of(gvr);
        if (st.text) {
            st.text = '';
            return 'filter';
        }
        if (st.selected.size) {
            table.clearSelection(gvr);
            return 'other';
        }
    }
    if (isDrawerOpen(router)) {
        void closeDrawer(router);
        return 'other';
    }
    return null;
}

export function installGlobalKeys(focusFilter: () => void): () => void {
    const handler = (e: KeyboardEvent) => {
        const ui = useUiStore();
        const clusters = useClustersStore();
        const sidebar = useSidebarStore();
        if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'k') {
            e.preventDefault();
            if (ui.paletteOpen || clusters.anyConnected) ui.paletteOpen = !ui.paletteOpen;
            return;
        }
        if (e.key === 'Escape') {
            if (ui.dialog || ui.paletteOpen || ui.createOpen) return; // those own Esc
            // Esc inside the YAML editor only leaves the editor; it must never close the drawer over unsaved edits.
            if (e.target instanceof HTMLElement && e.target.closest('.cm-editor')) {
                e.target.blur();
                return;
            }
            if (ui.commandBarOpen) {
                ui.commandBarOpen = false;
            } else if (ui.shortcutsOpen) {
                ui.shortcutsOpen = false;
            } else if (clusters.popoverOpen) {
                clusters.close();
            } else if (sidebar.open && !sidebar.pinned) {
                sidebar.open = false;
            } else {
                const fromList = !isEditable(e.target) && (e.target === document.body || !!listNav?.owns(e.target));
                const step = escapeStep();
                if (step === null) {
                    if (isEditable(e.target)) (e.target as HTMLElement).blur();
                } else if (step === 'filter' && fromList) {
                    // Esc from the list that cleared the filter stays on the list, on its first row.
                    void nextTick(() => listNav?.focusList(true));
                }
            }
            return;
        }
        if (e.ctrlKey || e.metaKey || e.altKey || e.defaultPrevented) return;
        if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.target instanceof HTMLInputElement && e.target.id === FILTER_ID) {
            // Enter in the filter hands focus to the results so the arrow keys move through them.
            if (!ui.commandBarOpen && !ui.shortcutsOpen && !ui.paletteOpen && !ui.createOpen && !ui.dialog && listNav?.focusList(false))
                e.preventDefault();
            return;
        }
        if (isEditable(e.target)) return;
        if (ui.commandBarOpen || ui.shortcutsOpen || ui.paletteOpen || ui.createOpen || ui.dialog) return;
        if (e.key === ':') {
            e.preventDefault();
            ui.commandBarOpen = true;
            return;
        }
        if (e.key === '/') {
            e.preventDefault();
            focusFilter();
            return;
        }
        if (e.key === '[') {
            e.preventDefault();
            // Auto-repeat would flip it back and forth; narrow screens have no sidebar to pin.
            if (!e.repeat && !matchMedia('(max-width: 899.98px)').matches) sidebar.togglePinned();
            return;
        }
        if (e.key === '?') {
            e.preventDefault();
            ui.shortcutsOpen = true;
            return;
        }
        if (clusters.popoverOpen) return;
        if (pageHandler?.(e)) e.preventDefault();
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
}
