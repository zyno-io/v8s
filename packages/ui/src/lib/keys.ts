import type { ObjectRef } from '@v8s/client';

import { ProblemsGVR } from '@v8s/client';
import { nextTick } from 'vue';

import router from '@/router.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useSidebarStore } from '@/stores/sidebar.ts';
import { useTableStore } from '@/stores/table.ts';
import { useUiStore } from '@/stores/ui.ts';

import { kinds } from './agent.ts';
import { closeDrawer, isDrawerOpen } from './drawer.ts';
import { recentNamespaces, sameSet } from './recent-namespaces.ts';

/** Where a key was pressed: the list (or no drawer), or the drawer (which owns the keyboard while shown). */
export type KeyScope = 'list' | 'drawer';
/** A page may claim j/k/x/Enter. Returns true when it handled the key. */
type PageKeyHandler = (e: KeyboardEvent, scope: KeyScope) => boolean;
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
    /** Hand focus back to the list after the drawer closes: the remembered row, else the closed object's row when loaded. */
    restoreFocus?(ref: ObjectRef | null): void;
}
let listNav: ListNav | null = null;

export function setListNav(n: ListNav | null) {
    listNav = n;
}

/** The drawer registers itself so keys pressed outside the list belong to it while it is shown. */
export interface DrawerNav {
    open(): boolean;
    /** Redirects a scroll key from the drawer's chrome into the active tab's scroller. True when it handled the key. */
    scrollKey(e: KeyboardEvent): boolean;
    /** Moves focus into the drawer (a list key opened the object already shown, so no object change takes it there). */
    focus(): void;
}
let drawerNav: DrawerNav | null = null;

export function setDrawerNav(n: DrawerNav | null) {
    drawerNav = n;
}
export function currentDrawerNav(): DrawerNav | null {
    return drawerNav;
}
export function focusDrawer(): void {
    drawerNav?.focus();
}

const keyScope = (t: EventTarget | null): KeyScope => (drawerNav?.open() && !listNav?.owns(t) ? 'drawer' : 'list');

export function restoreListFocus(ref: ObjectRef | null) {
    listNav?.restoreFocus?.(ref);
}

const FILTER_ID = 'pods-filter';

export function isEditable(t: EventTarget | null): boolean {
    if (!(t instanceof HTMLElement)) return false;
    if (t.isContentEditable) return true;
    const tag = t.tagName;
    // SmartSelect's trigger stands in for a native <select>, which the page shortcuts always left alone.
    if (tag === 'TEXTAREA' || tag === 'SELECT' || t.hasAttribute('data-select')) return true;
    if (tag === 'INPUT') {
        const type = (t as HTMLInputElement).type;
        return !(type === 'checkbox' || type === 'radio' || type === 'button');
    }
    return false;
}

/**
 * Esc order from the design system: an open drawer closes first (unless focus is in the page's search field), then the filter
 * clears, then the selection. `target` is where Esc was pressed.
 */
export function escapeChain(target: EventTarget | null = null): boolean {
    return escapeStep(target) !== null;
}

function isFilterField(t: EventTarget | null): boolean {
    return t instanceof HTMLElement && t.id === FILTER_ID;
}

function escapeStep(target: EventTarget | null): 'filter' | 'other' | null {
    // The drawer is the layer on top: Esc dismisses it, and the search stays as typed. Only Esc in the search field itself clears
    // the search (then blurs it), whether or not a drawer is open.
    if (!isFilterField(target) && isDrawerOpen(router)) {
        void closeDrawer(router);
        return 'other';
    }
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

/**
 * k9s-style namespace keys on the list on screen: 0 clears the namespace filter, 1–9 apply the recent sets (most recent first) without
 * reordering them. Not on cluster-scoped kinds; a digit with no set behind it does nothing.
 */
function namespaceKey(e: KeyboardEvent): boolean {
    const key = e.key;
    if (key.length !== 1 || key < '0' || key > '9') return false;
    const table = useTableStore();
    const gvr = table.currentGvr;
    // Problems has no namespace menu on screen, so a filter set there would be invisible.
    if (!gvr || gvr === ProblemsGVR || kinds.value.find(k => k.gvr === gvr)?.namespaced === false) return false;
    if (e.repeat) return true;
    const next = key === '0' ? [] : recentNamespaces()[Number(key) - 1];
    // Unchanged: no new array, which would reset the selection and re-query the view.
    if (next && !sameSet(next, table.namespaces)) table.namespaces = [...next];
    return true;
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
            // So do native <dialog> modals the store does not track (port-forward, drain, Helm): Esc closes only them.
            if (e.target instanceof HTMLElement && e.target.closest('dialog[open]')) return;
            // Esc inside an editable YAML editor only leaves the editor; it must never close the drawer over unsaved edits.
            // A read-only editor falls through to the normal Esc order.
            const cm = e.target instanceof HTMLElement ? e.target.closest('.cm-editor') : null;
            if (cm && cm.querySelector('.cm-content[contenteditable="true"]')) {
                (e.target as HTMLElement).blur();
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
                const step = escapeStep(e.target);
                if (step === null) {
                    if (isEditable(e.target)) (e.target as HTMLElement).blur();
                } else if (step === 'filter' && fromList) {
                    // Esc from the list that cleared the filter stays on the list, on its first row.
                    void nextTick(() => listNav?.focusList(true));
                }
            }
            return;
        }
        // Native <dialog> modals the store does not track (port-forward, ...) own the keyboard while open.
        if (e.target instanceof HTMLElement && e.target.closest('dialog[open]')) return;
        if (
            e.ctrlKey &&
            !e.metaKey &&
            !e.altKey &&
            !e.shiftKey &&
            !e.defaultPrevented &&
            e.key === 'd' &&
            !isEditable(e.target) &&
            !ui.commandBarOpen &&
            !ui.shortcutsOpen &&
            !ui.paletteOpen &&
            !ui.createOpen &&
            !ui.dialog &&
            !clusters.popoverOpen
        ) {
            // Ctrl+D is k9s' Delete; the page decides (and the browser must not bookmark either way while a list is on screen).
            if (pageHandler?.(e, keyScope(e.target))) e.preventDefault();
            return;
        }
        if (e.ctrlKey || e.metaKey || e.altKey || e.defaultPrevented) return;
        if (
            e.key === 'Enter' &&
            !e.shiftKey &&
            !e.isComposing &&
            e.keyCode !== 229 &&
            e.target instanceof HTMLInputElement &&
            e.target.id === FILTER_ID
        ) {
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
        const scope = keyScope(e.target);
        if (scope === 'drawer') {
            // Digits do nothing here: the namespace keys belong to the list.
            if (drawerNav!.scrollKey(e) || pageHandler?.(e, 'drawer')) e.preventDefault();
            return;
        }
        if (pageHandler?.(e, 'list') || namespaceKey(e)) e.preventDefault();
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
}
