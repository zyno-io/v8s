import { useClustersStore } from '@/stores/clusters.ts';
import { usePodsStore } from '@/stores/pods.ts';
import { useUiStore } from '@/stores/ui.ts';

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
    const pods = usePodsStore();
    const ui = useUiStore();
    if (pods.text) {
        pods.text = '';
        return true;
    }
    if (pods.selected.size) {
        pods.clearSelection();
        return true;
    }
    if (ui.drawer) {
        ui.drawer = null;
        return true;
    }
    return false;
}

export function installGlobalKeys(focusFilter: () => void): () => void {
    const handler = (e: KeyboardEvent) => {
        const ui = useUiStore();
        const clusters = useClustersStore();
        if (e.key === 'Escape') {
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
        if (ui.commandBarOpen || ui.shortcutsOpen) return;
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
