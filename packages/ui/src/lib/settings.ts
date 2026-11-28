import type { Settings } from '@v8s/client';

/** RFC 7386 JSON merge patch: `null` deletes a key, arrays and scalars replace, objects merge. Pure; never mutates its inputs. */
export function mergePatch<T>(target: T, patch: unknown): T {
    if (patch === null || typeof patch !== 'object' || Array.isArray(patch)) return patch as T;
    const base: Record<string, unknown> = target && typeof target === 'object' && !Array.isArray(target) ? { ...(target as object) } : {};
    for (const [k, v] of Object.entries(patch as Record<string, unknown>)) {
        if (v === null) delete base[k];
        else base[k] = mergePatch(base[k], v);
    }
    return base as T;
}

export function emptySettings(): Settings {
    return {
        groups: {},
        links: {},
        autoSwitchWrite: true,
        stars: [],
        bookmarks: [],
        selection: [],
        columnLayouts: {},
        problemPrefs: {},
        acks: {},
        uiPrefs: {},
        nodeShell: {},
        portForwards: [],
        protectedClusters: []
    };
}

/** Bookmarks are in-app paths: anything not starting with a single `/` (absolute or protocol-relative URLs, `javascript:`) is dropped. */
export function validBookmarks<T extends { url?: unknown }>(list: readonly T[]): T[] {
    return list.filter(b => !!b && typeof b.url === 'string' && b.url.startsWith('/') && !b.url.startsWith('//') && !b.url.startsWith('/\\'));
}

/** Fills missing collections so callers never null-check (the agent normalizes, but a patch result may not be complete). */
export function normalizeSettings(s: Partial<Settings> | null | undefined): Settings {
    const d = emptySettings();
    if (!s) return d;
    return {
        groups: s.groups ?? d.groups,
        links: s.links ?? d.links,
        autoSwitchWrite: s.autoSwitchWrite ?? d.autoSwitchWrite,
        stars: s.stars ?? d.stars,
        bookmarks: validBookmarks(s.bookmarks ?? d.bookmarks),
        selection: s.selection ?? d.selection,
        columnLayouts: s.columnLayouts ?? d.columnLayouts,
        problemPrefs: s.problemPrefs ?? d.problemPrefs,
        acks: s.acks ?? d.acks,
        uiPrefs: s.uiPrefs ?? d.uiPrefs,
        nodeShell: s.nodeShell ?? d.nodeShell,
        portForwards: s.portForwards ?? d.portForwards,
        protectedClusters: protectedClusters(s)
    };
}

// ---- stars ----

export function toggleStar(stars: readonly string[], gvr: string): string[] {
    return stars.includes(gvr) ? stars.filter(s => s !== gvr) : [...stars, gvr];
}

export function moveStar(stars: readonly string[], from: number, to: number): string[] {
    const out = [...stars];
    if (from < 0 || from >= out.length || to < 0 || to >= out.length) return out;
    const [x] = out.splice(from, 1);
    out.splice(to, 0, x);
    return out;
}

// ---- column layouts ----

export interface ColumnLayout {
    /** px width overrides by column key */
    widths?: Record<string, number>;
    /** column keys the user hid */
    hidden?: string[];
    /** default-hidden column keys (Column.hidden, decor defaults) the user showed from the Columns menu */
    shown?: string[];
}

export function layoutFor(s: Settings, gvr: string): ColumnLayout {
    const raw = s.columnLayouts?.[gvr];
    if (!raw || typeof raw !== 'object') return {};
    const l = raw as ColumnLayout;
    return {
        widths: l.widths && typeof l.widths === 'object' ? l.widths : undefined,
        hidden: Array.isArray(l.hidden) ? l.hidden : undefined,
        shown: Array.isArray(l.shown) ? l.shown : undefined
    };
}

export function layoutPatch(gvr: string, layout: ColumnLayout): Record<string, unknown> {
    return { columnLayouts: { [gvr]: { widths: layout.widths ?? {}, hidden: layout.hidden ?? [], shown: layout.shown ?? [] } } };
}

// ---- groups ----

export function groupPatch(name: string, ids: readonly string[]): Record<string, unknown> {
    return { groups: { [name]: [...ids] } };
}

export function deleteGroupPatch(name: string): Record<string, unknown> {
    return { groups: { [name]: null } };
}

export function renameGroupPatch(groups: Record<string, string[]>, from: string, to: string): Record<string, unknown> {
    const members = groups[from] ?? [];
    return { groups: { [from]: null, [to]: [...members] } };
}

/** Group names holding clusters, with their member ids, in name order. */
export function groupList(groups: Record<string, string[]>): [string, string[]][] {
    return Object.entries(groups)
        .map(([g, ids]): [string, string[]] => [g, [...(ids ?? [])]])
        .sort((a, b) => a[0].localeCompare(b[0]));
}

// ---- protected clusters ----

/** Cluster ids whose destructive actions ask for a typed confirmation. */
export function protectedClusters(s: Partial<Settings>): string[] {
    const v = s.protectedClusters as unknown;
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
}

export function protectedPatch(ids: readonly string[]): Record<string, unknown> {
    return { protectedClusters: [...ids] };
}

export function toggleProtected(ids: readonly string[], id: string): string[] {
    return ids.includes(id) ? ids.filter(x => x !== id) : [...ids, id];
}

// ---- dismissed link suggestions (acks) ----

export const LINK_DISMISS_KEY = 'linkDismissed';

export function dismissedLinks(s: Settings): Set<string> {
    const v = s.acks?.[LINK_DISMISS_KEY];
    return new Set(v && typeof v === 'object' ? Object.keys(v as object) : []);
}

export const linkKey = (read: string, write: string) => `${read}|${write}`;

export function dismissLinkPatch(read: string, write: string): Record<string, unknown> {
    return { acks: { [LINK_DISMISS_KEY]: { [linkKey(read, write)]: true } } };
}
