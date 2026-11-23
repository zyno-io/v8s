/**
 * Per-browser memory (localStorage): the cluster selection, the last visited page and namespace filters per cluster set.
 * The logic is pure; storage access is confined to `readJson` / `writeJson`, which never throw.
 */

export type Groups = Readonly<Record<string, readonly string[]>>;

/** What is remembered of a selection: a group name when it equalled that group's members, else the cluster ids. */
export type StoredSelection = { group: string } | { ids: string[] };

/** Namespace filters of one cluster set, per resource kind (gvr). Only non-empty filters are kept. */
export type NamespaceFilters = Record<string, string[]>;

export interface NamespaceEntry {
    key: string;
    ns: NamespaceFilters;
}

export const KEY_SELECTION = 'v8s:selection';
export const KEY_ROUTE = 'v8s:lastRoute';
export const KEY_NAMESPACES = 'v8s:namespaces';
export const KEY_YAML_WRAP = 'v8s:yamlWrap';
export const KEY_YAML_PRETTY = 'v8s:yamlPretty';
export const MAX_NAMESPACE_KEYS = 50;

const sameSet = (a: readonly string[], b: readonly string[]): boolean => {
    const s = new Set(a);
    return s.size === new Set(b).size && b.every(x => s.has(x));
};

/** The group whose (non-empty) members are exactly `ids`; the first by name when several match. */
export function matchGroup(ids: readonly string[], groups: Groups): string | null {
    if (!ids.length) return null;
    for (const name of Object.keys(groups).sort()) {
        const members = groups[name];
        if (members.length && sameSet(ids, members)) return name;
    }
    return null;
}

/** Identifies a connected set: `g:<group>` for an exact group match, else the sorted ids. Empty for an empty set. */
export function connectionKey(ids: readonly string[], groups: Groups): string {
    const g = matchGroup(ids, groups);
    if (g !== null) return 'g:' + g;
    return [...new Set(ids)].sort().join(',');
}

export function storeSelection(ids: readonly string[], groups: Groups): StoredSelection {
    const group = matchGroup(ids, groups);
    return group !== null ? { group } : { ids: [...ids] };
}

/** Parses a stored selection defensively; anything malformed is null. */
export function parseSelection(raw: unknown): StoredSelection | null {
    if (!raw || typeof raw !== 'object') return null;
    const r = raw as { group?: unknown; ids?: unknown };
    if (typeof r.group === 'string' && r.group) return { group: r.group };
    if (Array.isArray(r.ids) && r.ids.every(x => typeof x === 'string')) return { ids: r.ids as string[] };
    return null;
}

/**
 * The cluster ids to pre-check: a remembered group's current members when it still exists, else the remembered ids that still exist,
 * else the agent's own `selected` (also filtered to existing clusters).
 */
export function resolveChecked(
    stored: StoredSelection | null,
    groups: Groups,
    existing: ReadonlySet<string>,
    agentSelected: readonly string[]
): string[] {
    const keep = (ids: readonly string[]) => ids.filter(id => existing.has(id));
    if (stored) {
        const live = keep('group' in stored ? (groups[stored.group] ?? []) : stored.ids);
        if (live.length) return live;
    }
    return keep(agentSelected);
}

// ---- namespace filters per connection key ----

/** Drops empty filters; null when nothing is left. */
export function compactFilters(ns: Readonly<Record<string, readonly string[]>>): NamespaceFilters | null {
    const out: NamespaceFilters = {};
    for (const [gvr, list] of Object.entries(ns)) if (list.length) out[gvr] = [...list];
    return Object.keys(out).length ? out : null;
}

/** Puts `key` first (most recently used) with `ns`, or removes it when `ns` is null; keeps at most `max` entries. */
export function upsertEntry(
    entries: readonly NamespaceEntry[],
    key: string,
    ns: NamespaceFilters | null,
    max = MAX_NAMESPACE_KEYS
): NamespaceEntry[] {
    const rest = entries.filter(e => e.key !== key);
    return (ns ? [{ key, ns }, ...rest] : rest).slice(0, max);
}

/** Moves `key` to the front (a use, not a change); no-op when it has no entry. */
export function touchEntry(entries: readonly NamespaceEntry[], key: string): NamespaceEntry[] {
    const e = entries.find(x => x.key === key);
    return e ? upsertEntry(entries, key, e.ns) : [...entries];
}

/** Sets one kind's filter inside `key`'s entry, leaving its other kinds as stored (another tab may have changed them). */
export function setKindFilter(entries: readonly NamespaceEntry[], key: string, gvr: string, list: readonly string[]): NamespaceEntry[] {
    const ns: Record<string, readonly string[]> = { ...entries.find(e => e.key === key)?.ns };
    if (list.length) ns[gvr] = list;
    else delete ns[gvr];
    return upsertEntry(entries, key, compactFilters(ns));
}

/** The same cluster set under a new key (a group was created, renamed or deleted): `ns` replaces whatever `to` held, `from` is dropped. */
export function moveEntry(entries: readonly NamespaceEntry[], from: string, to: string, ns: NamespaceFilters | null): NamespaceEntry[] {
    return upsertEntry(
        entries.filter(e => e.key !== from),
        to,
        ns
    );
}

/** A group was renamed: its saved filters follow it (`g:<from>` → `g:<to>`); no-op when it has none. */
export function renameGroupEntries(entries: readonly NamespaceEntry[], from: string, to: string): NamespaceEntry[] {
    const e = entries.find(x => x.key === 'g:' + from);
    return e ? moveEntry(entries, e.key, 'g:' + to, e.ns) : [...entries];
}

/** renameGroupEntries on the stored list. */
export function renameStoredGroup(from: string, to: string): void {
    writeJson(KEY_NAMESPACES, renameGroupEntries(parseEntries(readJson(KEY_NAMESPACES)), from, to));
}

export function parseEntries(raw: unknown): NamespaceEntry[] {
    if (!Array.isArray(raw)) return [];
    const out: NamespaceEntry[] = [];
    for (const e of raw) {
        if (!e || typeof e.key !== 'string' || !e.ns || typeof e.ns !== 'object') continue;
        const valid: Record<string, string[]> = {};
        for (const [gvr, v] of Object.entries(e.ns as Record<string, unknown>)) {
            if (Array.isArray(v) && v.every(x => typeof x === 'string')) valid[gvr] = v as string[];
        }
        const ns = compactFilters(valid);
        if (ns) out.push({ key: e.key, ns });
    }
    return out.slice(0, MAX_NAMESPACE_KEYS);
}

export const sameFilters = (a: NamespaceFilters | null, b: NamespaceFilters | null): boolean => JSON.stringify(a) === JSON.stringify(b);

// ---- last visited route ----

/** Query keys that open the object drawer; they name an object of a cluster that may not be connected next time. */
const DRAWER_KEYS = ['c', 'ns', 'n', 'tab'];
/** One-shot query keys (the hosted signed-out splash) that must never be replayed from a remembered route. */
const ONE_SHOT_KEYS = ['signedout'];

/** The route worth remembering (path + query, drawer and one-shot keys dropped), or null for `/` and non-app paths. */
export function routeToRemember(path: string, query: Readonly<Record<string, unknown>>): string | null {
    if (path === '/' || !path.startsWith('/') || path.startsWith('//')) return null;
    const sp = new URLSearchParams();
    for (const [k, v] of Object.entries(query)) {
        if (DRAWER_KEYS.includes(k) || ONE_SHOT_KEYS.includes(k)) continue;
        for (const x of Array.isArray(v) ? v : [v]) if (typeof x === 'string') sp.append(k, x);
    }
    const q = sp.toString();
    return q ? `${path}?${q}` : path;
}

/** On the app's first navigation to a bare `/`, the remembered route to go to instead; otherwise null. */
export function restoreRoute(first: boolean, path: string, query: Readonly<Record<string, unknown>>, stored: unknown): string | null {
    if (!first || path !== '/' || Object.keys(query).length) return null;
    if (typeof stored !== 'string' || !stored.startsWith('/') || stored.startsWith('//') || stored.startsWith('/\\') || stored === '/') return null;
    return stored;
}

// ---- storage ----

export function readJson(key: string): unknown {
    try {
        const v = localStorage.getItem(key);
        return v === null ? null : JSON.parse(v);
    } catch {
        return null;
    }
}

export function writeJson(key: string, value: unknown): void {
    try {
        localStorage.setItem(key, JSON.stringify(value));
    } catch {
        // storage unavailable or full: the memory is a convenience
    }
}

/** A remembered on/off choice (default off); anything but a stored `true` is off. */
export const readFlag = (key: string): boolean => readJson(key) === true;
export const writeFlag = (key: string, on: boolean): void => writeJson(key, on);
