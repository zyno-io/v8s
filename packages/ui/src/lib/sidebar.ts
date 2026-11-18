import type { Settings } from '@v8s/client';

export const SIDEBAR_DEFAULT = 232;
export const SIDEBAR_MIN = 180;
export const SIDEBAR_MAX = 420;
/** Arrow keys on the resize handle move the width by this many px. */
export const SIDEBAR_STEP = 16;

export interface SidebarPrefs {
    pinned: boolean;
    width: number;
}

/** Rounds and bounds a width; anything that is not a finite number falls back to the default. */
export function clampSidebarWidth(w: unknown): number {
    if (typeof w !== 'number' || !Number.isFinite(w)) return SIDEBAR_DEFAULT;
    return Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, Math.round(w)));
}

/** Reads the sidebar prefs from an untrusted `uiPrefs` document; missing or malformed values fall back to pinned at the default width. */
export function sidebarPrefs(ui: Settings['uiPrefs'] | null | undefined): SidebarPrefs {
    return {
        pinned: typeof ui?.sidebarPinned === 'boolean' ? ui.sidebarPinned : true,
        width: clampSidebarWidth(ui?.sidebarWidth)
    };
}

/** A settings merge patch that sets (only) the given sidebar prefs. */
export function sidebarPatch(p: Partial<SidebarPrefs>): Record<string, unknown> {
    const ui: Record<string, unknown> = {};
    if (p.pinned !== undefined) ui.sidebarPinned = p.pinned;
    if (p.width !== undefined) ui.sidebarWidth = clampSidebarWidth(p.width);
    return { uiPrefs: ui };
}

const CACHE_KEY = 'v8s:sidebar';

/** First-paint cache so a reload does not flash the pinned sidebar before settings arrive (a per-viewer convenience only). */
export function readSidebarCache(): SidebarPrefs {
    try {
        const raw = localStorage.getItem(CACHE_KEY);
        if (raw) return sidebarPrefs(JSON.parse(raw) as Record<string, unknown>);
    } catch {
        // storage unavailable or malformed
    }
    return sidebarPrefs(null);
}

export function writeSidebarCache(p: SidebarPrefs) {
    try {
        localStorage.setItem(CACHE_KEY, JSON.stringify({ sidebarPinned: p.pinned, sidebarWidth: p.width }));
    } catch {
        // ignore
    }
}
