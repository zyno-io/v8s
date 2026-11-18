import type { IPGroup, IPHit, IPSearch, ObjectRef } from '@v8s/client';

/*
 * Pure helpers of IP search. The agent decides what an IP query is (agent/internal/ipx, `Parse`) and answers
 * with `SearchResult.ip`; `looksLikeIP` only mirrors its acceptance so the palette can hide Kinds and Commands
 * at once and `:po 10.42.` can be told from `:po payments`. Both are tested against
 * agent/internal/ipx/testdata/detect.json, so they cannot drift.
 */

const HEX = /^[0-9a-fA-F]{1,4}$/;
const BITS = /^(0|[1-9]\d*)$/;

interface Addr {
    v4: boolean;
    v: bigint;
}

function v4Octets(s: string): number[] | null {
    const p = s.split('.');
    if (p.length !== 4) return null;
    const out: number[] = [];
    for (const o of p) {
        if (!/^(0|[1-9]\d{0,2})$/.test(o) || Number(o) > 255) return null;
        out.push(Number(o));
    }
    return out;
}

function parseV6(s: string): bigint | null {
    if (!/^[0-9a-fA-F:.]+$/.test(s) || s.includes(':::')) return null;
    let tail: bigint[] = [];
    if (s.includes('.')) {
        const i = s.lastIndexOf(':');
        const o = v4Octets(s.slice(i + 1));
        if (i < 0 || !o) return null;
        tail = [BigInt(o[0] * 256 + o[1]), BigInt(o[2] * 256 + o[3])];
        s = s.slice(0, i + 1) + '0:0';
    }
    const groups = (part: string): bigint[] | null => {
        if (part === '') return [];
        const out: bigint[] = [];
        for (const g of part.split(':')) {
            if (!HEX.test(g)) return null;
            out.push(BigInt(parseInt(g, 16)));
        }
        return out;
    };
    const dc = s.indexOf('::');
    let all: bigint[];
    if (dc >= 0) {
        if (s.indexOf('::', dc + 1) >= 0) return null;
        const l = groups(s.slice(0, dc));
        const r = groups(s.slice(dc + 2));
        if (!l || !r || l.length + r.length > 7) return null;
        all = [...l, ...Array<bigint>(8 - l.length - r.length).fill(0n), ...r];
    } else {
        const g = groups(s);
        if (!g || g.length !== 8) return null;
        all = g;
    }
    if (tail.length) all = [...all.slice(0, 6), ...tail];
    return all.reduce((a, g) => (a << 16n) | g, 0n);
}

/** A full address, with IPv4-mapped IPv6 unmapped (as the agent stores it). */
function parseAddr(s: string): Addr | null {
    const o = v4Octets(s);
    if (o) return { v4: true, v: BigInt(((o[0] * 256 + o[1]) * 256 + o[2]) * 256 + o[3]) };
    const v = parseV6(s);
    if (v === null) return null;
    if (v >> 32n === 0xffffn) return { v4: true, v: v & 0xffffffffn };
    return { v4: false, v };
}

function stripZone(s: string): string {
    const i = s.indexOf('%');
    if (i < 0) return s;
    const j = s.indexOf('/', i);
    return j >= 0 ? s.slice(0, i) + s.slice(j) : s.slice(0, i);
}

function isRange(s: string): boolean {
    const i = s.indexOf('-');
    const l = parseAddr(s.slice(0, i).trim());
    let r = s.slice(i + 1).trim();
    const sl = r.indexOf('/');
    if (sl >= 0) r = r.slice(0, sl);
    const h = parseAddr(r);
    return !!l && !!h && l.v4 === h.v4 && l.v <= h.v;
}

function isCIDR(s: string): boolean {
    const i = s.indexOf('/');
    const bits = s.slice(i + 1);
    const addrText = s.slice(0, i);
    if (!BITS.test(bits)) return false;
    const o = v4Octets(addrText);
    const v6 = o ? null : parseV6(addrText);
    if (!o && v6 === null) return false;
    return Number(bits) <= (o ? 32 : 128);
}

function partialV4(s: string): boolean {
    const parts = s.split('.');
    const d = parts.length - 1;
    if (d < 1 || d > 3) return false;
    for (let i = 0; i < d; i++) {
        if (!/^(0|[1-9]\d{0,2})$/.test(parts[i]) || Number(parts[i]) > 255) return false;
    }
    const t = parts[d];
    if (t.length > 3 || !/^\d*$/.test(t) || (t.length > 1 && t[0] === '0') || (d === 3 && t !== '')) return false;
    if (t === '') return true;
    // at least one textual-prefix span must stay inside 0..255 (t="256" has none)
    const v = Number(t);
    return v <= 255 || (t.length < 3 && v * 10 <= 255) || (t.length < 2 && v * 100 <= 255);
}

function partialV6(s: string): boolean {
    if (!/^[0-9a-fA-F:]+$/.test(s)) return false;
    if ((s.match(/:/g) ?? []).length < 2 || s.includes(':::')) return false;
    let head: string;
    let t = '';
    if (s.endsWith('::')) {
        head = s.slice(0, -2);
    } else {
        if (s.includes('::')) return false;
        const i = s.lastIndexOf(':');
        head = s.slice(0, i);
        t = s.slice(i + 1);
    }
    if (head === '' || head.includes('::') || t.length > 4) return false;
    const groups = head.split(':');
    return groups.length <= 7 && groups.every(g => HEX.test(g));
}

/** Whether the agent will read `input` as an IP query (an address, a partial address, a CIDR or an a-b range). */
export function looksLikeIP(input: string): boolean {
    let s = input.trim();
    if (!s || s.length > 100) return false;
    if (s[0] === '[') {
        const i = s.indexOf(']');
        s = (i > 0 ? s.slice(1, i) : s.slice(1)).trim();
        if (!s) return false;
    }
    s = stripZone(s);
    if (s.includes('-')) return isRange(s);
    if (s.includes('/')) {
        if (!s.endsWith('/')) return isCIDR(s);
        s = s.slice(0, -1);
        if (!s || s.includes('/')) return false;
    }
    const ci = s.indexOf(':');
    if (ci > 0 && s.indexOf(':', ci + 1) < 0 && /^\d{1,5}$/.test(s.slice(ci + 1)) && v4Octets(s.slice(0, ci))) s = s.slice(0, ci);
    if (parseAddr(s) && !(parseV6(s) !== null && s.endsWith(':'))) return true;
    return partialV4(s) || partialV6(s);
}

export type IPAction = { type: 'open'; ref: ObjectRef } | { type: 'list'; gvr: string; text: string };

export interface IPItem {
    key: string;
    /** The section heading: `IP · Pods (3)`. */
    section: string;
    label: string;
    /** The matched address, span or range (shown as a mono chip). */
    value?: string;
    /** `Pod IP · contains · payments · service`. */
    detail: string;
    cluster?: string;
    /** An exact match. */
    strong?: boolean;
    testid: string;
    action: IPAction;
}

const MATCH_WORD: Record<string, string> = { contains: 'contains', within: 'in range', overlap: 'overlaps' };

/**
 * A readable plural of a Kind for headings: `IPAddressPool` → `IP address pools`, `EndpointSlice` → `endpoint slices`,
 * `ServiceCIDR` → `service CIDRs`. Acronyms keep their case; other words are lower-cased (the first is capitalized by
 * the caller when it starts a heading).
 */
export function kindPlural(kind: string): string {
    const words = kind
        .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
        .replace(/([A-Z]+)([A-Z][a-z])/g, '$1 $2')
        .split(' ')
        .filter(Boolean)
        .map(w => (w.length > 1 && w === w.toUpperCase() ? w : w.toLowerCase()));
    if (!words.length) return kind;
    const last = words[words.length - 1]!;
    words[words.length - 1] =
        last === last.toUpperCase() && last.length > 1
            ? last + 's'
            : /(s|x|ch|sh)$/.test(last)
              ? last + 'es'
              : /[^aeiou]y$/.test(last)
                ? last.slice(0, -1) + 'ies'
                : last + 's';
    return words.join(' ');
}

function plural(g: IPGroup): string {
    return kindPlural(g.kind);
}

function hitDetail(h: IPHit): string {
    return [h.label, MATCH_WORD[h.match], h.namespace, h.detail].filter(Boolean).join(' · ');
}

/** The palette rows of an IP result: each group's hits, then a "Show all" row when the group has more. */
export function ipItems(ip: IPSearch): IPItem[] {
    const out: IPItem[] = [];
    for (const g of ip.groups) {
        const pl = plural(g);
        const section = `IP · ${pl.replace(/^./, c => c.toUpperCase())} (${g.total})`;
        for (const h of g.hits) {
            out.push({
                key: `ip-${h.cluster}-${h.gvr}-${h.namespace ?? ''}-${h.name}`,
                section,
                label: h.name,
                value: h.value,
                detail: hitDetail(h),
                cluster: h.cluster,
                strong: h.match === 'exact',
                testid: 'palette-ip-hit',
                action: { type: 'open', ref: { cluster: h.cluster, gvr: h.gvr, namespace: h.namespace || undefined, name: h.name } }
            });
        }
        if (g.total > g.hits.length) {
            out.push({
                key: `ip-all-${g.gvr}`,
                section,
                label: `Show all ${g.total} ${pl} matching ${ip.query}`,
                detail: '',
                testid: 'palette-ip-showall',
                action: { type: 'list', gvr: g.gvr, text: ip.query }
            });
        }
    }
    return out;
}

/** A muted line while sources still load, or '' when none do. */
export function pendingText(ip: IPSearch | undefined): string {
    const n = ip?.pending?.length ?? 0;
    return n ? `Still loading ${n === 1 ? 'one source' : `${n} sources`}…` : '';
}

/** A muted line naming what was not searched, or '' when everything was. */
export function skippedText(ip: IPSearch | undefined): string {
    const s = ip?.skipped ?? [];
    if (!s.length) return '';
    const parts = s.slice(0, 3).map(x => `${x.kind || 'cluster'} on ${x.cluster}${x.reason ? ` (${x.reason})` : ''}`);
    return `Not searched: ${parts.join(', ')}${s.length > 3 ? ` and ${s.length - 3} more` : ''}`;
}
