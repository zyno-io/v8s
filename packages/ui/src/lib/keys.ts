import router from '@/router.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useTableStore } from '@/stores/table.ts';
import { useUiStore } from '@/stores/ui.ts';

import { closeDrawer, isDrawerOpen } from './drawer.ts';

/** A page may claim j/k/x/Enter. Returns true when it handled the key. */
type PageKeyHandler = (e: KeyboardEvent) => boolean;
let pageHandler: PageKeyHandler | null = null;

export function setPageKeyHandler(h: PageKeyHandler | null) {
    pageHandler = h;
}

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
    const table = useTableStore();
    const gvr = table.currentGvr;
    if (gvr) {
        const st = table.of(gvr);
        if (st.text) {
            st.text = '';
            return true;
        }
        if (st.selected.size) {
            table.clearSelection(gvr);
            return true;
        }
    }
    if (isDrawerOpen(router)) {
        void closeDrawer(router);
        return true;
    }
    return false;
}

export function installGlobalKeys(focusFilter: () => void): () => void {
    const handler = (e: KeyboardEvent) => {
        const ui = useUiStore();
        const clusters = useClustersStore();
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
            } else if (!escapeChain() && isEditable(e.target)) {
                (e.target as HTMLElement).blur();
            }
            return;
        }
        if (e.ctrlKey || e.metaKey || e.altKey || e.defaultPrevented) return;
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
