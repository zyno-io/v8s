import { MaskedValue } from '@v8s/client';
import { load } from 'js-yaml';
import { describe, expect, it } from 'vitest';

import { PRETTY_MAX, prettyKind, prettyYaml } from '../src/lib/pretty-yaml.ts';

const JSON_ON = { formatJson: true, decode: false };
const cm = (data: string) => `apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: n\ndata:\n${data}`;
const b64 = (s: string | Uint8Array) => Buffer.from(s).toString('base64');
const secret = (data: string) => `apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\ndata:\n${data}type: Opaque\n`;

describe('prettyKind', () => {
    it('knows ConfigMaps and Secrets only', () => {
        expect(prettyKind('v1/configmaps')).toBe('configmap');
        expect(prettyKind('v1/secrets')).toBe('secret');
        expect(prettyKind('apps/v1/deployments')).toBeNull();
    });
});

describe('prettyYaml ConfigMap', () => {
    it('indents compact JSON objects and arrays as block literals', async () => {
        const out = await prettyYaml(cm(`  a: '{"x":1,"y":[1,2]}'\n  b: '[{"id":1}]'\n`), 'configmap', JSON_ON);
        expect(out).toContain('  a: |-\n    {\n      "x": 1,');
        const d = load(out!) as { data: Record<string, string> };
        expect(JSON.parse(d.data.a)).toEqual({ x: 1, y: [1, 2] });
        expect(d.data.a).toBe(JSON.stringify({ x: 1, y: [1, 2] }, null, 2));
        expect(JSON.parse(d.data.b)).toEqual([{ id: 1 }]);
    });

    it('handles YAML double-quoted values and keeps a trailing newline', async () => {
        const out = await prettyYaml(cm(`  a: "{\\"x\\":1}\\n"\n`), 'configmap', JSON_ON);
        const d = load(out!) as { data: Record<string, string> };
        expect(d.data.a).toBe('{\n  "x": 1\n}\n');
        expect(out).toContain('a: |\n');
    });

    it('formats a double-encoded JSON string', async () => {
        const out = await prettyYaml(cm(`  a: '"{\\"k\\":[1]}"'\n`), 'configmap', JSON_ON);
        const d = load(out!) as { data: Record<string, string> };
        expect(JSON.parse(d.data.a)).toEqual({ k: [1] });
        expect(d.data.a).toContain('\n');
    });

    it('leaves invalid JSON, plain strings and quoted scalars untouched', async () => {
        const out = await prettyYaml(cm(`  a: '{not json}'\n  b: plain\n  c: '"just a string"'\n`), 'configmap', JSON_ON);
        const d = load(out ?? cm(`  a: '{not json}'\n  b: plain\n  c: '"just a string"'\n`)) as { data: Record<string, string> };
        expect(d.data).toEqual({ a: '{not json}', b: 'plain', c: '"just a string"' });
    });

    it('emits multi-line values as block literals without folding long lines', async () => {
        const long = 'word '.repeat(60).trim();
        const out = await prettyYaml(cm(`  conf: "listen 80;\\n${long}\\nend\\n"\n  one: x\n`), 'configmap', JSON_ON);
        expect(out).toContain('conf: |\n    listen 80;\n    ' + long + '\n    end\n');
        expect(out).not.toContain('\\n');
        expect((load(out!) as { data: Record<string, string> }).data.conf).toBe(`listen 80;\n${long}\nend\n`);
    });

    it('keeps key order and the rest of the document', async () => {
        const out = (await prettyYaml(cm(`  z: '{"a":1}'\n  a: '{"b":2}'\n`), 'configmap', JSON_ON))!;
        expect(out.indexOf('  z:')).toBeLessThan(out.indexOf('  a:'));
        const d = load(out) as { metadata: unknown; kind: string };
        expect(d.metadata).toEqual({ name: 'c', namespace: 'n' });
        expect(d.kind).toBe('ConfigMap');
    });

    it('does not touch binaryData', async () => {
        const out = await prettyYaml(cm(`  a: '{"x":1}'\nbinaryData:\n  b: ${b64('{"x":1}')}\n`), 'configmap', JSON_ON);
        expect((load(out!) as { binaryData: Record<string, string> }).binaryData.b).toBe(b64('{"x":1}'));
    });

    it('returns null when nothing changes, on parse failure and with the option off', async () => {
        expect(await prettyYaml(cm(`  a: plain\n`), 'configmap', JSON_ON)).toBeNull();
        expect(await prettyYaml('a: [unclosed', 'configmap', JSON_ON)).toBeNull();
        expect(await prettyYaml(cm(`  a: '{"x":1}'\n`), 'configmap', { formatJson: false, decode: false })).toBeNull();
        expect(await prettyYaml('apiVersion: v1\nkind: ConfigMap\n', 'configmap', JSON_ON)).toBeNull();
    });

    it('skips documents over the size guard', async () => {
        const big = cm(`  a: '{"x":1}'\n  pad: ${'x'.repeat(PRETTY_MAX)}\n`);
        expect(await prettyYaml(big, 'configmap', JSON_ON)).toBeNull();
        expect(PRETTY_MAX).toBe(256 * 1024);
    });
});

describe('prettyYaml Secret', () => {
    const both = { formatJson: true, decode: true };

    it('decodes text values into stringData, keeps binary in data', async () => {
        const bin = new Uint8Array([0xff, 0xfe, 0x00, 0x01]);
        const out = await prettyYaml(secret(`  token: ${b64('abc')}\n  blob: ${b64(bin)}\n`), 'secret', { formatJson: false, decode: true });
        const d = load(out!) as { data: Record<string, string>; stringData: Record<string, string>; type: string };
        expect(d.stringData).toEqual({ token: 'abc' });
        expect(d.data).toEqual({ blob: b64(bin) });
        expect(d.type).toBe('Opaque');
    });

    it('removes data when everything decodes, and puts stringData where data was', async () => {
        const out = (await prettyYaml(secret(`  a: ${b64('x')}\n`), 'secret', { formatJson: false, decode: true }))!;
        expect(load(out)).not.toHaveProperty('data');
        expect(out.indexOf('stringData:')).toBeLessThan(out.indexOf('type:'));
    });

    it('leaves control-character text in data', async () => {
        const out = await prettyYaml(secret(`  a: ${b64('x\u0001y')}\n  b: ${b64('tab\there\r\n')}\n`), 'secret', {
            formatJson: false,
            decode: true
        });
        const d = load(out!) as { data: Record<string, string>; stringData: Record<string, string> };
        expect(Object.keys(d.data)).toEqual(['a']);
        expect(d.stringData.b).toBe('tab\there\r\n');
    });

    it('decode-only mode leaves JSON compact; formatJson indents it', async () => {
        const src = secret(`  j: ${b64('{"a":1}')}\n`);
        const dec = load((await prettyYaml(src, 'secret', { formatJson: false, decode: true }))!) as { stringData: Record<string, string> };
        expect(dec.stringData.j).toBe('{"a":1}');
        const pretty = load((await prettyYaml(src, 'secret', both))!) as { stringData: Record<string, string> };
        expect(pretty.stringData.j).toBe('{\n  "a": 1\n}');
    });

    it('leaves masked values and non-base64 values alone', async () => {
        expect(await prettyYaml(secret(`  a: ${MaskedValue}\n`), 'secret', both)).toBeNull();
        expect(await prettyYaml(secret(`  a: "not base64!"\n`), 'secret', both)).toBeNull();
    });

    it('decodes around masked entries and does not clobber an existing stringData key', async () => {
        const src = `apiVersion: v1\nkind: Secret\ndata:\n  a: ${b64('x')}\n  m: ${MaskedValue}\n  c: ${b64('new')}\nstringData:\n  c: old\n`;
        const d = load((await prettyYaml(src, 'secret', { formatJson: false, decode: true }))!) as {
            data: Record<string, string>;
            stringData: Record<string, string>;
        };
        expect(d.data).toEqual({ m: MaskedValue, c: b64('new') });
        expect(d.stringData).toEqual({ c: 'old', a: 'x' });
    });

    it('decodes multi-byte UTF-8', async () => {
        const out = await prettyYaml(secret(`  a: ${b64('héllo ✓')}\n`), 'secret', { formatJson: false, decode: true });
        expect((load(out!) as { stringData: Record<string, string> }).stringData.a).toBe('héllo ✓');
    });

    // What the API server does with a decoded draft: stringData text back to base64, UTF-8 bytes as written.
    const reencode = (yaml: string): Record<string, string> => {
        const d = load(yaml) as { stringData?: Record<string, string>; data?: Record<string, string> };
        const out: Record<string, string> = { ...d.data };
        for (const [k, v] of Object.entries(d.stringData ?? {})) out[k] = b64(v);
        return out;
    };

    it('keeps a leading BOM (and a BOM-only value) byte for byte', async () => {
        const src = { bom: '\ufeffhello', only: '\ufeff', mid: 'a\ufeffb' };
        const y = secret(
            Object.entries(src)
                .map(([k, v]) => `  ${k}: ${b64(v)}\n`)
                .join('')
        );
        const out = await prettyYaml(y, 'secret', { formatJson: false, decode: true });
        const back = reencode(out ?? y);
        for (const [k, v] of Object.entries(src)) expect(back[k]).toBe(b64(v));
    });

    it('decodes keys named like Object.prototype members and __proto__', async () => {
        const y = secret(`  __proto__: ${b64('p')}\n  constructor: ${b64('c')}\n  toString: ${b64('t')}\n  hasOwnProperty: ${b64('h')}\n`);
        const out = (await prettyYaml(y, 'secret', { formatJson: false, decode: true }))!;
        expect(out).toBe(
            y.slice(0, y.indexOf('\ndata:') + 1) + 'stringData:\n  __proto__: p\n  constructor: c\n  toString: t\n  hasOwnProperty: h\ntype: Opaque\n'
        );
        expect(out).not.toContain('\ndata:');
    });

    it('never decodes a value whose text is the masking placeholder', async () => {
        const y = secret(`  a: ${b64(MaskedValue)}\n  b: ${b64('ok')}\n`);
        const d = load((await prettyYaml(y, 'secret', { formatJson: false, decode: true }))!) as {
            data: Record<string, string>;
            stringData: Record<string, string>;
        };
        expect(d.data).toEqual({ a: b64(MaskedValue) });
        expect(d.stringData).toEqual({ b: 'ok' });
    });

    it('quotes radix-looking keys and values so they stay strings', async () => {
        const y = secret(`  0X1F: ${b64('0B1')}\n  0o17: ${b64('0O17')}\n  k: ${b64('-0XFF')}\n`);
        const out = (await prettyYaml(y, 'secret', { formatJson: false, decode: true }))!;
        expect(out).toContain("'0X1F': '0B1'");
        expect(out).toContain("'0o17': '0O17'");
        expect(out).toContain("k: '-0XFF'");
    });

    it('leaves every line outside data/stringData byte for byte', async () => {
        const y =
            "apiVersion: v1\nkind: Secret\nmetadata:\n  annotations:\n    n: 0X1F\n    big: '12345678901234567890'\n    t: 2001-12-14\n  labels:\n    on: 'yes'\n  generation: 9007199254740993\n  name: s\n" +
            `data:\n  a: ${b64('x')}\n  blob: ${b64(new Uint8Array([0xff, 0xfe]))}\nimmutable: false\ntype: Opaque\n`;
        const out = (await prettyYaml(y, 'secret', { formatJson: false, decode: true }))!;
        const idx = y.indexOf('\ndata:') + 1;
        expect(out.startsWith(y.slice(0, idx))).toBe(true);
        expect(out.endsWith('immutable: false\ntype: Opaque\n')).toBe(true);
        expect(out).toContain('data:\n  blob: ');
        expect(out.indexOf('stringData:\n  a: x')).toBeGreaterThan(out.indexOf('blob:'));
    });

    it('puts stringData right after data, or where it already was', async () => {
        const fresh = (await prettyYaml(secret(`  a: ${b64('x')}\n  z: ${b64(new Uint8Array([0xff]))}\n`), 'secret', {
            formatJson: false,
            decode: true
        }))!;
        expect(fresh).toMatch(/data:\n {2}z: .*\nstringData:\n {2}a: x\ntype: Opaque/);
        const y = `apiVersion: v1\ndata:\n  a: ${b64('x')}\nkind: Secret\nstringData:\n  s: old\ntype: Opaque\n`;
        const out = (await prettyYaml(y, 'secret', { formatJson: false, decode: true }))!;
        expect(out).toBe('apiVersion: v1\nkind: Secret\nstringData:\n  s: old\n  a: x\ntype: Opaque\n');
    });

    it('does not rewrite a document with non-string values or several documents', async () => {
        expect(await prettyYaml(`kind: Secret\ndata:\n  a: 1\n`, 'secret', { formatJson: false, decode: true })).toBeNull();
        expect(await prettyYaml(`kind: Secret\ndata:\n  a: ${b64('x')}\n---\nkind: X\n`, 'secret', { formatJson: false, decode: true })).toBeNull();
    });
});

describe('prettyYaml untouched parts and block literals', () => {
    it('keeps everything outside ConfigMap data byte for byte', async () => {
        const y = `apiVersion: v1\nbinaryData:\n  b: AAEC\ndata:\n  a: '{"x":1}'\nkind: ConfigMap\nmetadata:\n  annotations:\n    n: 0X1F\n    ts: 2001-12-14\n  generation: 9007199254740993\n  name: c\n`;
        const out = (await prettyYaml(y, 'configmap', JSON_ON))!;
        expect(out).toBe(y.replace(`  a: '{"x":1}'\n`, '  a: |-\n    {\n      "x": 1\n    }\n'));
    });

    it('quotes radix-looking ConfigMap keys and values', async () => {
        const out = await prettyYaml(cm(`  0X1F: '0B1'\n  m: "a\\nb"\n`), 'configmap', JSON_ON);
        expect(out).toContain("'0X1F': '0B1'");
    });

    it('emits multi-line values with tabs as block literals, but not ones with a CR or a leading tab', async () => {
        const out = (await prettyYaml(cm(`  mk: "all:\\n\\tgo build\\n"\n  win: "a\\r\\nb\\r\\n"\n  lead: "\\tx\\ny"\n`), 'configmap', JSON_ON))!;
        expect(out).toContain('mk: |\n    all:\n    \tgo build\n');
        expect(out).toContain('win: "a\\r\\nb\\r\\n"');
        expect(out).toContain('lead: "\\tx\\ny"');
        const d = load(out) as { data: Record<string, string> };
        expect(d.data).toEqual({ mk: 'all:\n\tgo build\n', win: 'a\r\nb\r\n', lead: '\tx\ny' });
    });

    it('quotes underscore-laden numbers that Go would still read as numbers', async () => {
        const y = secret(`  0_x1F: ${b64('1_e3')}\n  -_1: ${b64('+_1.')}\n  1_000: ${b64('plain_text')}\n`);
        const out = (await prettyYaml(y, 'secret', { formatJson: false, decode: true }))!;
        expect(out).toContain("'0_x1F': '1_e3'");
        expect(out).toContain("'-_1': '+_1.'");
        expect(out).toContain("'1_000': plain_text");
    });

    it('keeps the final newline when a section is the last top-level key', async () => {
        const y = `apiVersion: v1\nkind: Secret\ndata:\n  a: ${b64('x')}\n`;
        expect(await prettyYaml(y, 'secret', { formatJson: false, decode: true })).toBe('apiVersion: v1\nkind: Secret\nstringData:\n  a: x\n');
        const cmLast = `apiVersion: v1\nkind: ConfigMap\ndata:\n  a: '{"x":1}'\n`;
        expect((await prettyYaml(cmLast, 'configmap', JSON_ON))!.endsWith('    }\n')).toBe(true);
    });
});
