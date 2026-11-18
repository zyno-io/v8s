import type { Column } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import { gridTemplate, planColumns } from '../src/lib/columns.ts';

const cols: Column[] = [
    { key: 'cluster', type: 'dict' },
    { key: 'name', type: 'str' },
    { key: 'namespace', type: 'dict' },
    { key: 'ready', type: 'str', label: 'Ready', width: 60 },
    { key: 'replicas', type: 'int', label: 'Replicas' },
    { key: 'statusTone', type: 'tone' },
    { key: 'createdAt', type: 'time', label: 'Age' }
];

describe('planColumns', () => {
    it('hides the cluster column for a single cluster and the tone column behind the status text', () => {
        const p = planColumns(cols, { gvr: 'apps/v1/deployments', showCluster: false });
        expect(p.cols.map(c => c.key)).toEqual(['name', 'namespace', 'ready', 'replicas', 'createdAt']);
        expect(p.cols.find(c => c.key === 'ready')!.kind).toBe('status');
        expect(p.toneIdx).toBe(7);
        expect(p.nameIdx).toBe(3);
        expect(p.hasTime).toBe(true);
    });

    it('shows the cluster column first for several clusters', () => {
        const p = planColumns(cols, { gvr: 'apps/v1/deployments', showCluster: true });
        expect(p.cols[0]).toMatchObject({ key: 'cluster', kind: 'cluster', idx: 2 });
    });

    it('right-aligns ints, formats times as ages, and uses Column.label and width', () => {
        const p = planColumns(cols, { gvr: 'x', showCluster: false });
        expect(p.cols.find(c => c.key === 'replicas')).toMatchObject({ align: 'right', kind: 'int', label: 'Replicas' });
        expect(p.cols.find(c => c.key === 'createdAt')).toMatchObject({ kind: 'age' });
        expect(p.cols.find(c => c.key === 'ready')!.width).toBe(60);
    });

    it('applies the saved layout: hidden columns and pixel widths (a resized name stops growing)', () => {
        const p = planColumns(cols, { gvr: 'x', showCluster: false, layout: { hidden: ['namespace'], widths: { name: 300, replicas: 90 } } });
        expect(p.cols.map(c => c.key)).not.toContain('namespace');
        const name = p.cols.find(c => c.key === 'name')!;
        expect(name).toMatchObject({ width: 300, grow: false });
        expect(p.cols.find(c => c.key === 'replicas')!.width).toBe(90);
        expect(p.grid.startsWith('15px 300px')).toBe(true);
        // the name column can not be hidden
        expect(planColumns(cols, { gvr: 'x', showCluster: false, layout: { hidden: ['name'] } }).cols.map(c => c.key)).toContain('name');
    });

    it('falls back to a tone tag when no status text column exists', () => {
        const p = planColumns(
            [
                { key: 'name', type: 'str' },
                { key: 'statusTone', type: 'tone' }
            ],
            { gvr: 'x', showCluster: false }
        );
        expect(p.cols.map(c => c.kind)).toEqual(['name', 'tone']);
    });

    it('folds pod containers into ready', () => {
        const pods: Column[] = [
            { key: 'name', type: 'str' },
            { key: 'ready', type: 'int' },
            { key: 'containers', type: 'int' },
            { key: 'status', type: 'dict' },
            { key: 'statusTone', type: 'tone' }
        ];
        const p = planColumns(pods, { gvr: 'v1/pods', showCluster: false });
        expect(p.cols.map(c => c.key)).toEqual(['name', 'ready', 'status']);
        expect(p.cols[1].kind).toBe('ready');
        expect(p.cols[2].kind).toBe('status');
    });

    it('returns an empty plan before the schema arrives', () => {
        expect(planColumns(null, { gvr: 'x', showCluster: false }).cols).toEqual([]);
    });
});

describe('gridTemplate', () => {
    it('overrides one column width during a drag', () => {
        const p = planColumns(cols, { gvr: 'x', showCluster: false });
        expect(gridTemplate(p.cols)).toContain('minmax(160px, 1.7fr)');
        expect(gridTemplate(p.cols, { key: 'name', width: 250 }).startsWith('15px 250px')).toBe(true);
    });
});

// The pods schema of agent/pkg/protocol PodColumns (the order and the metadata matter, not the labels of the plain columns).
const u = (key: string, label: string, unit: string, o: Partial<Column> = {}): Column => ({ key, type: 'int', label, unit, align: 'right', ...o });
const PODS: Column[] = [
    { key: 'cluster', type: 'dict', label: 'Cluster', width: 110 },
    { key: 'name', type: 'str', label: 'Name', width: 320 },
    { key: 'namespace', type: 'dict', label: 'Namespace', width: 120 },
    { key: 'ready', type: 'int', label: 'Ready', width: 64 },
    { key: 'containers', type: 'int', label: 'Containers', width: 84 },
    { key: 'status', type: 'dict', label: 'Status', width: 120 },
    { key: 'statusTone', type: 'tone', label: 'Tone', width: 40 },
    { key: 'restarts', type: 'int', label: 'Restarts', width: 68 },
    u('cpuUse', 'CPU', 'millicores', { metrics: true }),
    u('cpuReq', 'CPU req', 'millicores', { short: 'req', hidden: true }),
    u('cpuLim', 'CPU lim', 'millicores', { short: 'lim', hidden: true }),
    u('cpuPctReq', '%CPU/R', 'permille', { short: '%R', metrics: true }),
    u('cpuPctLim', '%CPU/L', 'permille', { short: '%L', metrics: true }),
    u('cpuPctNode', '%CPU/N', 'permille', { short: '%N', metrics: true, hidden: true }),
    u('memUse', 'MEM', 'MiB', { metrics: true }),
    u('memReq', 'MEM req', 'MiB', { short: 'req', hidden: true }),
    u('memLim', 'MEM lim', 'MiB', { short: 'lim', hidden: true }),
    u('memPctReq', '%MEM/R', 'permille', { short: '%R', metrics: true }),
    u('memPctLim', '%MEM/L', 'permille', { short: '%L', metrics: true }),
    u('memPctNode', '%MEM/N', 'permille', { short: '%N', metrics: true, hidden: true }),
    { key: 'node', type: 'dict', label: 'Node', width: 120 },
    { key: 'ip', type: 'str', label: 'IP', width: 120, hidden: true },
    { key: 'createdAt', type: 'time', label: 'Age', width: 56, align: 'right' }
];

describe('planColumns: the usage block', () => {
    const plan = (o: Partial<Parameters<typeof planColumns>[1]> = {}) => planColumns(PODS, { gvr: 'v1/pods', showCluster: false, ...o });

    it('collapses the visible unit columns into one block at the first one, in schema order', () => {
        const p = plan();
        expect(p.cols.map(c => c.key)).toEqual(['name', 'namespace', 'ready', 'status', 'restarts', 'usage', 'node', 'createdAt']);
        const b = p.cols.find(c => c.kind === 'usage')!;
        expect(b.subs!.map(x => x.key)).toEqual(['cpuUse', 'cpuPctReq', 'cpuPctLim', 'memUse', 'memPctReq', 'memPctLim']);
        expect(b.idx).toBe(10);
        expect(b.grow).toBe(false);
        expect(b.sortable).toBe(false);
    });

    it('is 274 px wide with a wider gap where cpu meets mem', () => {
        const b = plan().cols.find(c => c.kind === 'usage')!;
        expect(b.width).toBe(274);
        expect(b.subGrid).toBe('44px 36px 36px 56px 36px 36px');
        expect(b.subs!.find(x => x.key === 'memUse')!.lead).toBe(true);
        expect(b.subs!.filter(x => x.lead)).toHaveLength(1);
    });

    it('uses short labels when the block shows the raw column of that resource, full labels otherwise', () => {
        const b = plan().cols.find(c => c.kind === 'usage')!;
        expect(b.subs!.map(x => x.text)).toEqual(['CPU', '%R', '%L', 'MEM', '%R', '%L']);
        expect(b.subs!.map(x => x.suffix)).toEqual(['m', '', '', 'Mi', '', '']);
        expect(b.subs!.map(x => x.label)).toEqual(['CPU', '%CPU/R', '%CPU/L', 'MEM', '%MEM/R', '%MEM/L']);
        const noRaw = plan({ layout: { hidden: ['cpuUse'] } }).cols.find(c => c.kind === 'usage')!;
        expect(noRaw.subs!.map(x => x.text)).toEqual(['%CPU/R', '%CPU/L', 'MEM', '%R', '%L']);
        expect(noRaw.subs![0].width).toBeGreaterThan(36); // a full label needs more than a bare percentage
    });

    it('colours %/L and %/N cells by rule and marks percentages of a node', () => {
        const subs = plan({ layout: { shown: ['cpuPctNode'] } }).cols.find(c => c.kind === 'usage')!.subs!;
        expect(subs.map(x => [x.key, x.tone, x.pctNode])).toEqual([
            ['cpuUse', null, false],
            ['cpuPctReq', null, false],
            ['cpuPctLim', 'lim', false],
            ['cpuPctNode', 'node', true],
            ['memUse', null, false],
            ['memPctReq', null, false],
            ['memPctLim', 'lim', false]
        ]);
    });

    it('hides Column.hidden columns by default and shows them only when the user lists them in `shown`', () => {
        expect(plan().visibleKeys.has('ip')).toBe(false);
        expect(plan().defaultHidden.has('cpuReq')).toBe(true);
        const p = plan({ layout: { shown: ['ip', 'cpuReq'] } });
        expect(p.cols.map(c => c.key)).toContain('ip');
        expect(p.cols.find(c => c.kind === 'usage')!.subs!.map(x => x.key)).toContain('cpuReq');
        // hidden wins over shown
        expect(plan({ layout: { shown: ['ip'], hidden: ['ip'] } }).cols.map(c => c.key)).not.toContain('ip');
    });

    it('a layout saved by a resize (hidden: []) no longer reveals the default-hidden columns', () => {
        const p = plan({ layout: { widths: { name: 300 }, hidden: [] } });
        expect(p.cols.map(c => c.key)).not.toContain('ip');
        expect(p.cols.find(c => c.kind === 'usage')!.subs).toHaveLength(6);
    });

    it('the decor default-hidden keys follow the same rule', () => {
        const d = { defaultHidden: ['node'] };
        expect(plan({ decor: d }).cols.map(c => c.key)).not.toContain('node');
        expect(plan({ decor: d, layout: { hidden: [] } }).cols.map(c => c.key)).not.toContain('node');
        expect(plan({ decor: d, layout: { shown: ['node'] } }).cols.map(c => c.key)).toContain('node');
    });

    it('metricsOff drops the metrics columns, keeps request/limit ones, and lists the metrics ones with a note', () => {
        const p = plan({ metricsOff: true, layout: { shown: ['cpuReq', 'cpuLim'] } });
        expect(p.cols.find(c => c.kind === 'usage')!.subs!.map(x => x.key)).toEqual(['cpuReq', 'cpuLim']);
        expect(p.hasMetrics).toBe(false);
        expect(p.all.find(c => c.key === 'cpuUse')!.note).toMatch(/metrics-server/);
        expect(p.all.find(c => c.key === 'cpuReq')!.note).toBeUndefined();
        expect(plan({ metricsOff: true }).cols.some(c => c.kind === 'usage')).toBe(false);
    });

    it('knows which keys need usage (a sort on a hidden metrics column still does) and whether any is on screen', () => {
        const p = plan();
        expect(p.hasMetrics).toBe(true);
        expect(p.metricKeys).toContain('cpuPctNode');
        expect(p.metricKeys).not.toContain('cpuReq');
        expect(plan({ layout: { hidden: PODS.filter(c => c.metrics).map(c => c.key) } }).hasMetrics).toBe(false);
    });

    it('exposes the row indexes for the tooltip and the phone card', () => {
        const p = plan();
        expect(p.usage).toMatchObject({ cpuUse: 10, cpuReq: 11, cpuLim: 12, cpuPctLim: 14, memUse: 16, memPctLim: 20, node: 22 });
        expect(planColumns(cols, { gvr: 'x', showCluster: false }).usage).toBeNull();
    });

    it('gives the name column 217 px (at least 200) at 1440 px with the sidebar pinned (content 1166)', () => {
        const p = plan();
        let fixed = 15 + 24; // checkbox column and row padding
        let n = 0;
        for (const c of p.cols) {
            n++;
            if (!c.grow) fixed += c.width;
        }
        const gaps = 12 * n; // n tracks plus the checkbox column: n gaps
        const name = 1166 - fixed - gaps;
        expect(name).toBe(217);
    });
});
