import { MaskedValue } from '@v8s/client';

import { PARSE_LIMIT } from './object-session.ts';

/**
 * Readable renderings of ConfigMap and Secret YAML. Pure text-to-text; js-yaml is a lazy chunk, loaded only when a Pretty view is on.
 *
 * - formatJson: JSON values (also double-encoded ones) are indented. This changes the stored bytes, so it is for viewing only.
 * - decode (Secrets): base64 `data` values that are valid text move to `stringData` as text. Lossless: the API server turns
 *   `stringData` back into `data`, so this form is what a Secret is edited in.
 *
 * Only the top-level `data` and `stringData` sections are rewritten; every other line of the document is kept byte for byte.
 * Multi-line strings come out as block literals (except ones with a CR, which stay quoted) and lines are never folded.
 */

export type PrettyKind = 'configmap' | 'secret';
export interface PrettyOptions {
    formatJson: boolean;
    decode: boolean;
}

/** Documents over this many characters are left as they are: no big parse on the main thread (same limit as the Overview's parse). */
export const PRETTY_MAX = PARSE_LIMIT;

export function prettyKind(gvr: string): PrettyKind | null {
    return gvr === 'v1/configmaps' ? 'configmap' : gvr === 'v1/secrets' ? 'secret' : null;
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => !!v && typeof v === 'object' && !Array.isArray(v);

const bracketed = (t: string) => (t.startsWith('{') && t.endsWith('}')) || (t.startsWith('[') && t.endsWith(']'));
// A 16+ digit number cannot survive JSON.parse exactly; leave such documents alone rather than change a value.
const BIG_NUMBER = /\d{16,}/;

function parseContainer(t: string): unknown {
    if (!bracketed(t) || BIG_NUMBER.test(t)) return undefined;
    try {
        const v: unknown = JSON.parse(t);
        return v !== null && typeof v === 'object' ? v : undefined;
    } catch {
        return undefined;
    }
}

/** The indented form of a JSON object/array text (also one encoded as a JSON string), or null when it is not one. */
export function formatJsonText(s: string): string | null {
    const t = s.trim();
    let v = parseContainer(t);
    if (v === undefined && t.length > 1 && t.startsWith('"') && t.endsWith('"')) {
        try {
            const inner: unknown = JSON.parse(t);
            if (typeof inner === 'string') v = parseContainer(inner.trim());
        } catch {
            // not a JSON string
        }
    }
    if (v === undefined) return null;
    return JSON.stringify(v, null, 2) + (s.endsWith('\n') ? '\n' : '');
}

const OK_CONTROL = new Set([9, 10, 13]);

/** The text a base64 value holds, or null when it is not base64 or not UTF-8 text without control characters. */
export function decodeBase64Text(b64: string): string | null {
    let bin: string;
    try {
        bin = atob(b64.replace(/\s+/g, ''));
    } catch {
        return null;
    }
    const bytes = Uint8Array.from(bin, c => c.charCodeAt(0));
    let text: string;
    try {
        text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
    } catch {
        return null;
    }
    for (let i = 0; i < text.length; i++) {
        const c = text.charCodeAt(i);
        if ((c < 32 && !OK_CONTROL.has(c)) || c === 127) return null;
    }
    return text;
}

const dictOf = (m: Obj): Obj => Object.assign(Object.create(null) as Obj, m);
const allStrings = (m: Obj) => Object.values(m).every(v => typeof v === 'string');

function formatValues(m: Obj): boolean {
    let changed = false;
    for (const k of Object.keys(m)) {
        const v = m[k];
        if (typeof v !== 'string' || v === MaskedValue) continue;
        const f = formatJsonText(v);
        if (f !== null && f !== v) {
            m[k] = f;
            changed = true;
        }
    }
    return changed;
}

/** Text of the Secret `data` entries that can move to `stringData` (and what is left); null when nothing moves. */
function decodeSecret(data: Obj, sd: Obj): { rest: Obj; sd: Obj } | null {
    const rest = dictOf({});
    const out = dictOf(sd);
    let moved = false;
    for (const k of Object.keys(data)) {
        const v = data[k];
        // A value that decodes to the masking placeholder must stay in data: the agent would read it as "unchanged" and drop the key.
        const text = typeof v === 'string' && v !== MaskedValue && !Object.hasOwn(out, k) ? decodeBase64Text(v) : null;
        if (text === null || text === MaskedValue) rest[k] = v;
        else {
            out[k] = text;
            moved = true;
        }
    }
    return moved ? { rest, sd: out } : null;
}

// ---- the document is edited as text: only the top-level `data` / `stringData` blocks are replaced ----

interface Section {
    start: number;
    end: number;
}
const MARKER = /^(---|\.\.\.)(\s|$)/;

/** Line ranges of the top-level `data:` and `stringData:` blocks (a block runs to the next column-0 key line); null for a multi-document text. */
function findSections(lines: string[]): { data?: Section; stringData?: Section } | null {
    const found: { data?: Section; stringData?: Section } = {};
    let cur: 'data' | 'stringData' | null = null;
    let start = 0;
    const close = (end: number) => {
        // Trailing empty lines belong to the document, not the block: the last block must not swallow the final newline.
        while (end > start + 1 && lines[end - 1] === '') end--;
        if (cur) found[cur] = { start, end };
        cur = null;
    };
    for (let i = 0; i < lines.length; i++) {
        const l = lines[i];
        if (MARKER.test(l)) return null;
        if (!l || /^[\s#-]/.test(l)) continue;
        close(i);
        const m = /^(data|stringData):(\s|$)/.exec(l);
        if (m) {
            if (found[m[1] as 'data' | 'stringData']) return null;
            cur = m[1] as 'data' | 'stringData';
            start = i;
        }
    }
    close(lines.length);
    return found;
}

const sectionObject = (text: string, load: JsYaml['load'], FAILSAFE_SCHEMA: JsYaml['FAILSAFE_SCHEMA'], key: string): Obj | null => {
    let doc: unknown;
    try {
        doc = load(text, { schema: FAILSAFE_SCHEMA });
    } catch {
        return null;
    }
    const v = isObj(doc) ? doc[key] : null;
    return isObj(v) && allStrings(v) ? v : null;
};

/**
 * The prettified YAML, or null when there is nothing to show differently: not a ConfigMap/Secret, too large, unparsable,
 * or no value changed.
 */
export async function prettyYaml(yaml: string, kind: PrettyKind, opts: PrettyOptions): Promise<string | null> {
    if (yaml.length > PRETTY_MAX || (!opts.formatJson && !(kind === 'secret' && opts.decode))) return null;
    const js = await import('js-yaml');
    const lines = yaml.split('\n');
    const sec = findSections(lines);
    if (!sec) return null;
    const get = (s: Section | undefined, key: string) =>
        s ? sectionObject(lines.slice(s.start, s.end).join('\n'), js.load, js.FAILSAFE_SCHEMA, key) : null;
    const rules = scalarRules(js);
    const emit = (key: string, m: Obj) =>
        Object.keys(m).length ? js.dump({ [key]: m }, { lineWidth: -1, seqNoIndent: true, scalarStyleRules: rules }) : '';

    let dataText: string | null = null; // replacement for the data block
    let sdText: string | null = null; // replacement for the stringData block
    if (kind === 'configmap') {
        const data = get(sec.data, 'data');
        if (!data) return null;
        const m = dictOf(data);
        // Even with no JSON in it, the dump turns quoted multi-line values into block literals.
        if (!formatValues(m) && !Object.values(m).some(v => typeof v === 'string' && v.includes('\n'))) return null;
        dataText = emit('data', m);
    } else {
        let data = get(sec.data, 'data');
        let sd = get(sec.stringData, 'stringData');
        if (sec.data && !data) return null;
        if (sec.stringData && !sd) return null;
        let changed = false;
        if (opts.decode && data) {
            const d = decodeSecret(data, sd ?? {});
            if (d) {
                data = d.rest;
                sd = d.sd;
                dataText = emit('data', data);
                changed = true;
            }
        }
        if (opts.formatJson && sd) {
            const m = dictOf(sd);
            if (formatValues(m)) {
                sd = m;
                changed = true;
            }
        }
        if (!changed || !sd) return null;
        sdText = emit('stringData', sd);
    }

    const out: string[] = [];
    let i = 0;
    const blocks: [Section | undefined, string | null][] = [
        [sec.data, dataText],
        [sec.stringData, sdText]
    ];
    const edits = blocks.filter((b): b is [Section, string] => !!b[0] && b[1] !== null).sort((a, b) => a[0].start - b[0].start);
    for (const [s, text] of edits) {
        out.push(...lines.slice(i, s.start));
        if (text) out.push(...text.replace(/\n$/, '').split('\n'));
        // A new stringData block goes right after the data block it came from.
        if (s === sec.data && sdText !== null && !sec.stringData && sdText) out.push(...sdText.replace(/\n$/, '').split('\n'));
        i = s.end;
    }
    out.push(...lines.slice(i));
    const text = out.join('\n');
    return text === yaml ? null : text;
}

type JsYaml = typeof import('js-yaml');

/** js-yaml's default scalar rules plus: multi-line values with tabs as literal blocks, and Go-ambiguous plain scalars quoted. */
function scalarRules(js: JsYaml) {
    const { DEFAULT_SCALAR_STYLE_RULES: D, SCALAR_STYLE: S } = js;
    // Tabs are legal in block scalar content; js-yaml quotes any value with one. A CR (or other invisible) keeps the quoted form,
    // and so does a value whose first non-empty line starts with a tab (Go's parser rejects that as indentation).
    const tabLiteral: (typeof D)[keyof typeof D] = l => {
        const v = l.node.value;
        if (
            l.style !== S.PLAIN ||
            l.isKey ||
            !v.includes('\n') ||
            !v.includes('\t') ||
            /^\s*\t/.test(v) ||
            /[\r\x7F-\xA0\u2028\u2029\uFEFF\uFFFE\uFFFF]/.test(v)
        )
            return;
        if ((l.allowedStylesMask & (1 << S.LITERAL_BLOCK)) !== 0) l.style = S.LITERAL_BLOCK;
    };
    // sigs.k8s.io/yaml reads ints with ParseInt(s, 0, 64), which accepts 0X/0O/0B prefixes that js-yaml leaves plain.
    // It also drops `_` before parsing numbers, so `0_x1F`, `1_e3` and `-_1` are numbers (or renamed keys) too.
    const quoteGo: (typeof D)[keyof typeof D] = l => {
        const v = l.node.value;
        const p = v.replace(/_/g, '');
        if (
            l.style === S.PLAIN &&
            /^[-+0-9.]/.test(v) &&
            (/^[-+]?0[xob]/i.test(p) || /^[-+]?[0-9]+$/.test(p) || /^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$/.test(p))
        )
            l.style = S.SINGLE_QUOTED;
    };
    const rules: (typeof D)[keyof typeof D][] = [];
    for (const [name, rule] of Object.entries(D)) {
        if (name === 'doubleQuoteForInvisibles') rules.push(tabLiteral);
        if (name === 'quoteInvalidPlain') rules.push(quoteGo);
        rules.push(rule);
    }
    return rules;
}
