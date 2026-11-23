/**
 * Recently used namespace filters, k9s style: keys 1–9 apply the 9 most recent sets, 0 clears the filter. A set is recorded when the
 * user picks it explicitly (a command naming namespaces, or closing the namespace menu after changing it); applying one with a digit
 * never reorders the list, so 1/2/1/2 flips between the same two sets. Kept per connected cluster set, like the filters themselves.
 * The list logic is pure; `recordRecentNamespaces` / `recentNamespaces` read and write storage.
 */

import { MAX_NAMESPACE_KEYS, readJson, writeJson } from './remember.ts';

export const KEY_RECENT_NAMESPACES = 'v8s:recentNamespaces';
export const MAX_RECENT = 9;

export interface RecentEntry {
    key: string;
    sets: string[][];
}

/** Same namespaces, in any order. */
export const sameSet = (a: readonly string[], b: readonly string[]): boolean => {
    const s = new Set(a);
    return s.size === new Set(b).size && b.every(x => s.has(x));
};

/** `set` becomes the most recent (moved there when already listed, in any order); the oldest falls off past `max`. Empty: no-op. */
export function pushRecent(sets: readonly (readonly string[])[], set: readonly string[], max = MAX_RECENT): string[][] {
    const copy = sets.map(s => [...s]);
    if (!set.length) return copy;
    return [[...new Set(set)], ...copy.filter(s => !sameSet(s, set))].slice(0, max);
}

export function parseRecent(raw: unknown): RecentEntry[] {
    if (!Array.isArray(raw)) return [];
    const out: RecentEntry[] = [];
    for (const e of raw) {
        if (!e || typeof e.key !== 'string' || !Array.isArray(e.sets)) continue;
        const sets = (e.sets as unknown[]).filter((s): s is string[] => Array.isArray(s) && s.length > 0 && s.every(x => typeof x === 'string'));
        if (sets.length) out.push({ key: e.key, sets: sets.slice(0, MAX_RECENT) });
    }
    return out.slice(0, MAX_NAMESPACE_KEYS);
}

/** Records `set` for the cluster set `key` and makes `key` the most recently used. */
export function withRecent(entries: readonly RecentEntry[], key: string, set: readonly string[]): RecentEntry[] {
    if (!key || !set.length) return [...entries];
    const sets = pushRecent(entries.find(e => e.key === key)?.sets ?? [], set);
    return [{ key, sets }, ...entries.filter(e => e.key !== key)].slice(0, MAX_NAMESPACE_KEYS);
}

/**
 * The same cluster set under a new key (a group was created, renamed or deleted): its list follows, replacing what `to` held. `keep`
 * leaves a copy under `from` (a group whose members were edited keeps its own list for when it is connected again).
 */
export function moveRecent(entries: readonly RecentEntry[], from: string, to: string, keep = false): RecentEntry[] {
    const e = entries.find(x => x.key === from);
    if (!e || from === to) return [...entries];
    const rest = entries.filter(x => x.key !== to && (keep || x.key !== from));
    return [{ key: to, sets: e.sets }, ...rest].slice(0, MAX_NAMESPACE_KEYS);
}

// ---- storage, keyed by the current cluster set (namespace-memory.ts sets it) ----

let currentKey = '';

export function setRecentKey(key: string): void {
    currentKey = key;
}

const stored = (): RecentEntry[] => parseRecent(readJson(KEY_RECENT_NAMESPACES));

export function recordRecentNamespaces(set: readonly string[]): void {
    if (!currentKey || !set.length) return;
    writeJson(KEY_RECENT_NAMESPACES, withRecent(stored(), currentKey, set));
}

/** The recent sets of the current cluster set, most recent first. */
export function recentNamespaces(): string[][] {
    return stored().find(e => e.key === currentKey)?.sets ?? [];
}

export const hasStoredRecent = (key: string): boolean => stored().some(e => e.key === key);

export function moveStoredRecent(from: string, to: string, keep = false): void {
    const entries = stored();
    if (entries.some(e => e.key === from)) writeJson(KEY_RECENT_NAMESPACES, moveRecent(entries, from, to, keep));
}
