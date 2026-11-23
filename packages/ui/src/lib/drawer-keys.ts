/** Pure helpers for routing scroll keys from the drawer's chrome into the active tab's scroller. */

export const SCROLL_KEYS = ['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'PageUp', 'PageDown', 'Home', 'End'] as const;

export function isScrollKey(e: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey'>): boolean {
    return !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && (SCROLL_KEYS as readonly string[]).includes(e.key);
}

/** Where focus is inside the drawer: its root, the tab panel, `<body>`, a tab button, or another control in the chrome. */
export type DrawerSpot = 'root' | 'panel' | 'body' | 'tab' | 'control';

const VERTICAL = ['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown'];

/** On a tab button ← → Home End switch tabs (ARIA); everything else moves into the scroller. */
export function redirectsScrollKey(spot: DrawerSpot, key: string): boolean {
    return spot === 'tab' ? VERTICAL.includes(key) : (SCROLL_KEYS as readonly string[]).includes(key);
}

export type ScrollStep = { top: number } | { left: number } | { to: 'start' | 'end' };

const LINE = 40;

export function scrollStep(key: string, viewH: number): ScrollStep {
    const page = Math.max(LINE, Math.round(viewH * 0.875));
    switch (key) {
        case 'ArrowUp':
            return { top: -LINE };
        case 'ArrowDown':
            return { top: LINE };
        case 'ArrowLeft':
            return { left: -LINE };
        case 'ArrowRight':
            return { left: LINE };
        case 'PageUp':
            return { top: -page };
        case 'PageDown':
            return { top: page };
        case 'Home':
            return { to: 'start' };
        default:
            return { to: 'end' };
    }
}
