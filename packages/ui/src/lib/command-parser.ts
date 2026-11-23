import type { Kind } from '@v8s/client';

import { rank } from './fuzzy.ts';
import { looksLikeIP } from './ip-search.ts';
import { preferKind } from './kinds.ts';

export interface ClusterRef {
    id: string;
    name: string;
    groups: string[];
}

export interface CommandContext {
    kinds: readonly Kind[];
    /** Namespaces from the `v1/namespaces` view. */
    namespaces: readonly string[];
    clusters: readonly ClusterRef[];
    /** Cluster ids currently connected. */
    selected: readonly string[];
    /** The current view's namespace filter (completes `:ns -<namespace>`). */
    filter?: readonly string[];
}

export type Command =
    | { type: 'empty' }
    | { type: 'ctx-open' }
    | { type: 'ctx-set'; ids: string[] }
    | { type: 'ctx-delta'; add: string[]; remove: string[] }
    /**
     * `namespaces` replaces the kind's namespace filter (`[]` clears it: `:po *`); absent, the filter is kept. `namespace` + `name` open
     * one object (`:po ns/name`).
     */
    | { type: 'open-kind'; gvr: string; plural: string; namespaces?: string[]; namespace?: string; name?: string; text?: string }
    | { type: 'ip'; text: string }
    /** `namespaces: null` clears the filter (`:ns all`, `:ns *`). */
    | { type: 'ns'; namespaces: string[] | null }
    | { type: 'ns-delta'; add: string[]; remove: string[] }
    | { type: 'problems' }
    | { type: 'star'; on: boolean }
    | { type: 'quit' }
    | { type: 'error'; message: string };

export interface Completion {
    /** Text shown in the list. */
    label: string;
    detail?: string;
    /** The full input text after accepting this completion. */
    insert: string;
}

const COMMANDS: { name: string; aliases: string[]; takesArgs: boolean; detail: string }[] = [
    { name: 'ctx', aliases: ['context'], takesArgs: true, detail: 'choose clusters' },
    { name: 'ip', aliases: [], takesArgs: true, detail: 'find objects by IP' },
    { name: 'ns', aliases: ['namespace'], takesArgs: true, detail: 'namespaces, or set the filter: ns <name> | +<name> | -<name> | all' },
    { name: 'pb', aliases: ['problems'], takesArgs: false, detail: 'open problems' },
    { name: 'star', aliases: [], takesArgs: false, detail: 'star the current type' },
    { name: 'unstar', aliases: [], takesArgs: false, detail: 'unstar the current type' },
    { name: 'q', aliases: ['quit'], takesArgs: false, detail: 'close the bar' }
];

const NS_USAGE = 'usage: :ns <ns>[,<ns>…] | +<ns> | -<ns> | all | *   (* matches any text: pay*)';

/** `all` or a lone `*`: every namespace (clears the filter). */
const isAllNs = (item: string): boolean => item === '*' || item.toLowerCase() === 'all';

/** `a,b , c` → ['a', 'b', 'c']. */
function splitList(s: string): string[] {
    return s
        .split(',')
        .map(x => x.trim())
        .filter(Boolean);
}

/** A `*` glob as an anchored regex (`*` = any text, everything else literal). */
function globRe(glob: string): RegExp {
    return new RegExp(
        '^' +
            glob
                .split('*')
                .map(p => p.replace(/[.+?^${}()|[\]\\]/g, '\\$&'))
                .join('.*') +
            '$'
    );
}

/**
 * A namespace or a `*` pattern as names: a plain name stays as typed (it may not be loaded yet); a pattern expands against `pool`
 * (what v8s knows now, so a namespace created later is not added) and is an error when nothing matches.
 */
function expandNs(item: string, pool: readonly string[], what: string): string[] | string {
    if (!item.includes('*')) return [item];
    const re = globRe(item);
    const m = pool.filter(n => re.test(n));
    if (m.length) return m;
    return pool.length ? `no ${what} matches "${item}"` : `no ${what} matches "${item}" (none loaded yet)`;
}

function tokenize(input: string): string[] {
    return input.trim().split(/\s+/).filter(Boolean);
}

function commandFor(head: string): (typeof COMMANDS)[number] | undefined {
    const h = head.toLowerCase();
    return COMMANDS.find(c => c.name === h || c.aliases.includes(h));
}

/** Names a kind can be typed as: plural, kind, short names (lowercase). */
function kindNames(k: Kind): string[] {
    return [k.plural, k.kind, ...(k.shortNames ?? [])].map(s => s.toLowerCase());
}

export type KindLookup = { kind: Kind } | { ambiguous: string[] } | null;

export function findKind(head: string, kinds: readonly Kind[]): KindLookup {
    const h = head.toLowerCase();
    const exact = preferKind(kinds.filter(k => kindNames(k).includes(h)));
    if (exact) return { kind: exact };
    const pref = kinds.filter(k => kindNames(k).some(n => n.startsWith(h)));
    if (pref.length === 1) return { kind: pref[0] };
    if (pref.length > 1) return { ambiguous: pref.map(k => k.plural) };
    return null;
}

function resolveCluster(token: string, clusters: readonly ClusterRef[]): string[] | null {
    const t = token.toLowerCase();
    const one = clusters.find(c => c.id.toLowerCase() === t || c.name.toLowerCase() === t);
    if (one) return [one.id];
    const group = clusters.filter(c => c.groups.some(g => g.toLowerCase() === t));
    return group.length ? group.map(c => c.id) : null;
}

export function parseCommand(input: string, ctx: CommandContext): Command {
    const tokens = tokenize(input);
    if (!tokens.length) return { type: 'empty' };
    const head = tokens[0];
    const args = tokens.slice(1);
    const cmd = commandFor(head);

    if (cmd?.name === 'ctx') {
        if (!args.length) return { type: 'ctx-open' };
        const deltas = args.filter(a => a[0] === '+' || a[0] === '-');
        if (deltas.length && deltas.length !== args.length) return { type: 'error', message: 'cannot mix names with +name / -name' };
        const resolve = (tokensIn: string[], sign: string) => {
            const out: string[] = [];
            for (const t of tokensIn) {
                const name = sign ? t.slice(1) : t;
                const ids = resolveCluster(name, ctx.clusters);
                if (!ids) return `unknown cluster "${name}"`;
                out.push(...ids);
            }
            return out;
        };
        if (deltas.length) {
            const add = resolve(
                deltas.filter(a => a[0] === '+'),
                '+'
            );
            const remove = resolve(
                deltas.filter(a => a[0] === '-'),
                '-'
            );
            if (typeof add === 'string') return { type: 'error', message: add };
            if (typeof remove === 'string') return { type: 'error', message: remove };
            return { type: 'ctx-delta', add, remove };
        }
        const ids = resolve(args, '');
        if (typeof ids === 'string') return { type: 'error', message: ids };
        return { type: 'ctx-set', ids: [...new Set(ids)] };
    }
    if (cmd?.name === 'ip') {
        const text = args.join(' ');
        return text ? { type: 'ip', text } : { type: 'error', message: 'usage: :ip <address|cidr|range>' };
    }
    if (cmd?.name === 'ns') {
        if (!args.length && !/\s$/.test(input)) {
            // Bare `:ns` (no trailing space: `ns ` is still choosing a namespace) opens the Namespaces list, like `:namespaces`.
            const k = findKind('namespaces', ctx.kinds);
            if (k && 'kind' in k && k.kind.available) return { type: 'open-kind', gvr: k.kind.gvr, plural: k.kind.plural };
            return { type: 'error', message: NS_USAGE };
        }
        // `:ns +a -b …` adds to and removes from the filter instead of replacing it, like `:ctx +name / -name`. Each argument may be a
        // comma list, and any name may be a `*` pattern (`:ns +pay*,shop`, `:ns -kube-*`): `+` patterns match the known namespaces,
        // `-` patterns the ones in the current filter.
        const deltas = args.filter(a => a[0] === '+' || a[0] === '-');
        if (deltas.length) {
            if (deltas.length !== args.length) return { type: 'error', message: 'cannot mix names with +namespace / -namespace' };
            const add: string[] = [];
            const remove: string[] = [];
            for (const a of deltas) {
                const items = splitList(a.slice(1));
                if (!items.length) return { type: 'error', message: NS_USAGE };
                for (const it of items) {
                    const r = a[0] === '+' ? expandNs(it, ctx.namespaces, 'namespace') : expandNs(it, ctx.filter ?? [], 'namespace in the filter');
                    if (typeof r === 'string') return { type: 'error', message: r };
                    (a[0] === '+' ? add : remove).push(...r);
                }
            }
            return { type: 'ns-delta', add: [...new Set(add)], remove: [...new Set(remove)] };
        }
        const r = namespaceList(args.flatMap(splitList), ctx);
        return typeof r === 'string' ? { type: 'error', message: r } : { type: 'ns', namespaces: r };
    }
    if (cmd?.name === 'pb') return { type: 'problems' };
    if (cmd?.name === 'star') return { type: 'star', on: true };
    if (cmd?.name === 'unstar') return { type: 'star', on: false };
    if (cmd?.name === 'q') return { type: 'quit' };

    const found = findKind(head, ctx.kinds);
    if (!found) return { type: 'error', message: `unknown command "${head}"` };
    if ('ambiguous' in found) return { type: 'error', message: `"${head}" is ambiguous: ${found.ambiguous.slice(0, 5).join(', ')}` };
    const kind = found.kind;
    if (!kind.available) return { type: 'error', message: `${kind.kind} (${kind.plural}) is not available yet` };
    const arg = args[0];
    const open = { type: 'open-kind', gvr: kind.gvr, plural: kind.plural } as const;
    if (!arg) return open;
    if (!kind.namespaced && isAllNs(arg)) return open;
    // A namespace name has no dots or colons, so an IP-like argument is an address filter (kinds with IPs only).
    if (kind.ips && looksLikeIP(arg)) return { type: 'open-kind', gvr: kind.gvr, plural: kind.plural, text: arg };
    if (!kind.namespaced) return { type: 'open-kind', gvr: kind.gvr, plural: kind.plural, name: arg };
    const slash = arg.indexOf('/');
    if (slash >= 0) {
        const namespace = arg.slice(0, slash);
        const name = arg.slice(slash + 1);
        if (!namespace || !name) return { type: 'error', message: 'usage: :<type> <namespace>/<name>' };
        return { type: 'open-kind', gvr: kind.gvr, plural: kind.plural, namespace, name };
    }
    // Namespaces the way `:ns` takes them: `:po a,b`, `:po a, b`, `:po pay*`, `:po *`.
    const r = namespaceList(args.flatMap(splitList), ctx);
    if (typeof r === 'string') return { type: 'error', message: r };
    return { ...open, namespaces: r ?? [] };
}

/** A `:ns`-style namespace list: null for all (`all`, `*`), the expanded names, or an error message. */
function namespaceList(items: readonly string[], ctx: CommandContext): string[] | null | string {
    if (!items.length) return NS_USAGE;
    if (items.some(isAllNs)) return items.length === 1 ? null : '"all" cannot be combined with namespaces';
    const out: string[] = [];
    for (const it of items) {
        const r = expandNs(it, ctx.namespaces, 'namespace');
        if (typeof r === 'string') return r;
        out.push(...r);
    }
    return [...new Set(out)];
}

/**
 * Completion for the text after the `:`. Prefix matches first, then fuzzy, over commands, kinds, namespaces and clusters.
 * Each item's `insert` is the whole new input, so Tab can just replace it.
 */
export function complete(input: string, ctx: CommandContext, limit = 8): Completion[] {
    const trailingSpace = /\s$/.test(input);
    const tokens = tokenize(input);
    const completingHead = tokens.length === 0 || (tokens.length === 1 && !trailingSpace);

    if (completingHead) {
        const q = tokens[0] ?? '';
        const entries: Completion[] = [];
        const seen = new Set<string>();
        for (const c of COMMANDS) {
            entries.push({ label: c.name, detail: c.detail, insert: c.name + (c.takesArgs ? ' ' : '') });
            seen.add(c.name);
        }
        for (const k of ctx.kinds) {
            for (const name of kindNames(k)) {
                if (seen.has(name)) continue;
                seen.add(name);
                entries.push({
                    label: name,
                    detail: k.available ? k.kind : `${k.kind} (not available yet)`,
                    insert: name + (k.namespaced ? ' ' : '')
                });
            }
        }
        return rank(q, entries, e => e.label).slice(0, limit);
    }

    const head = tokens[0];
    const lastIdx = trailingSpace ? tokens.length : tokens.length - 1;
    const typed = trailingSpace ? '' : tokens[tokens.length - 1];
    const before = tokens.slice(0, lastIdx).join(' ') + ' ';
    const cmd = commandFor(head);

    if (cmd?.name === 'ctx') {
        const sign = typed[0] === '+' || typed[0] === '-' ? typed[0] : '';
        const q = sign ? typed.slice(1) : typed;
        const used = new Set(tokens.slice(1, lastIdx).map(t => t.replace(/^[+-]/, '').toLowerCase()));
        const pool: Completion[] = [];
        for (const c of ctx.clusters) {
            if (used.has(c.id.toLowerCase())) continue;
            const on = ctx.selected.includes(c.id);
            pool.push({ label: sign + c.name, detail: on ? 'connected' : c.groups.join(', ') || undefined, insert: before + sign + c.name });
        }
        const groups = new Set(ctx.clusters.flatMap(c => c.groups));
        for (const g of groups) {
            if (used.has(g.toLowerCase())) continue;
            pool.push({ label: sign + g, detail: 'group', insert: before + sign + g });
        }
        return rank(q, pool, e => e.label.replace(/^[+-]/, '')).slice(0, limit);
    }

    if (cmd?.name === 'ip') return [];
    if (cmd?.name !== 'ns' && lastIdx === 1 && looksLikeIP(typed)) return [];
    if (cmd?.name === 'ns') return completeNs(typed, tokens.slice(1, lastIdx), before, lastIdx, ctx, limit, true);
    const kindHit = !cmd ? findKind(head, ctx.kinds) : null;
    if (kindHit && 'kind' in kindHit && kindHit.kind.namespaced && lastIdx === 1 && !typed.includes('/')) {
        return completeNs(typed, [], before, lastIdx, ctx, limit, false);
    }
    return [];
}

/**
 * Completion for `:ns` and `:<type> <ns>`: the part after the last comma of the token being typed. `+` offers namespaces not in the
 * filter yet, `-` the ones in it (`:ns` only: `deltas`), neither repeats one already typed; a `*` pattern shows how many namespaces it
 * matches.
 */
function completeNs(
    typed: string,
    prior: readonly string[],
    before: string,
    lastIdx: number,
    ctx: CommandContext,
    limit: number,
    deltas: boolean
): Completion[] {
    const sign = deltas && (typed[0] === '+' || typed[0] === '-') ? typed[0] : '';
    const body = typed.slice(sign.length);
    const cut = body.lastIndexOf(',') + 1;
    const lead = before + sign + body.slice(0, cut);
    const seg = body.slice(cut);
    const inFilter = new Set(ctx.filter ?? []);
    const names = sign === '-' ? [...inFilter] : sign === '+' ? ctx.namespaces.filter(n => !inFilter.has(n)) : ctx.namespaces;
    // A lone `*` means all namespaces, which cannot be combined with names.
    if (seg === '*' && !sign) return lastIdx === 1 && !cut ? [{ label: '*', detail: 'all namespaces', insert: before + typed }] : [];
    if (seg.includes('*')) {
        const re = globRe(seg);
        const m = names.filter(n => re.test(n));
        const shown = m.slice(0, 3).join(', ') + (m.length > 3 ? ', …' : '');
        return [{ label: sign + seg, detail: m.length ? `matches ${m.length}: ${shown}` : 'matches nothing', insert: before + typed }];
    }
    const used = new Set([...prior, sign + body.slice(0, cut)].flatMap(t => splitList(t.replace(/^[+-]/, ''))));
    const pool: Completion[] = [];
    if (!sign && lastIdx === 1 && !cut) pool.push({ label: 'all', detail: 'all namespaces (clears the filter)', insert: before + 'all' });
    for (const ns of names) {
        if (!used.has(ns)) pool.push({ label: sign + ns, detail: sign === '-' ? 'in the filter' : 'namespace', insert: lead + ns });
    }
    return rank(seg, pool, e => e.label.replace(/^[+-]/, '')).slice(0, limit);
}
