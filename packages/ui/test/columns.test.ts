import type { Column } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import { gridTemplate, guessPodsUsage, guessUsage, planColumns } from '../src/lib/columns.ts';

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
    { key: 'vpa', type: 'dict', label: 'VPA', width: 36 },
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
        expect(p.cols.map(c => c.key)).toEqual(['name', 'namespace', 'ready', 'status', 'restarts', 'vpa', 'usage', 'node', 'createdAt']);
        const b = p.cols.find(c => c.kind === 'usage')!;
        expect(b.subs!.map(x => x.key)).toEqual(['cpuUse', 'cpuPctReq', 'cpuPctLim', 'memUse', 'memPctReq', 'memPctLim']);
        expect(b.idx).toBe(11);
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

    it('colors %/L and %/N cells by rule and marks percentages of a node', () => {
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
        expect(p.usage).toMatchObject({ cpuUse: 11, cpuReq: 12, cpuLim: 13, cpuPctLim: 15, memUse: 17, memPctLim: 21, node: 23, vpa: 10 });
        expect(planColumns(cols, { gvr: 'x', showCluster: false }).usage).toBeNull();
    });

    it('gives the name column 169 px (the 36 px VPA column takes 48) at 1440 px with the sidebar pinned (content 1166)', () => {
        const p = plan();
        let fixed = 15 + 24; // checkbox column and row padding
        let n = 0;
        for (const c of p.cols) {
            n++;
            if (!c.grow) fixed += c.width;
        }
        const gaps = 12 * n; // n tracks plus the checkbox column: n gaps
        const name = 1166 - fixed - gaps;
        expect(name).toBe(169);
    });
});

// The nodes schema of agent/internal/ingest registerClusterKinds.
const NODES: Column[] = [
    { key: 'cluster', type: 'dict', label: 'Cluster', width: 110 },
    { key: 'name', type: 'str', label: 'Name', width: 320 },
    { key: 'status', type: 'dict', label: 'Status', width: 132 },
    { key: 'roles', type: 'dict', label: 'Roles', width: 110 },
    { key: 'version', type: 'dict', label: 'Version', width: 84 },
    { key: 'internalIP', type: 'str', label: 'Internal IP', width: 116 },
    { key: 'os', type: 'dict', label: 'OS image', width: 180, hidden: true },
    { key: 'kernel', type: 'dict', label: 'Kernel', width: 130, hidden: true },
    { key: 'runtime', type: 'dict', label: 'Runtime', width: 140, hidden: true },
    u('cpuUse', 'CPU', 'millicores', { metrics: true }),
    u('cpuPctAlloc', '%CPU', 'permille', { short: '%', metrics: true }),
    u('cpu', 'CPU alloc', 'millicores', { short: 'alloc' }),
    u('memUse', 'MEM', 'MiB', { metrics: true }),
    u('memPctAlloc', '%MEM', 'permille', { short: '%', metrics: true }),
    u('memory', 'MEM alloc', 'MiB', { short: 'alloc' }),
    { key: 'pods', type: 'int', label: 'Pods', width: 52, align: 'right' },
    { key: 'taints', type: 'int', label: 'Taints', width: 64, hidden: true },
    { key: 'statusTone', type: 'tone', label: 'Tone', width: 40 },
    { key: 'createdAt', type: 'time', label: 'Age', width: 56, align: 'right' }
];

describe('planColumns: the nodes usage block', () => {
    const plan = (o: Partial<Parameters<typeof planColumns>[1]> = {}) => planColumns(NODES, { gvr: 'v1/nodes', showCluster: false, ...o });

    it('hides os, kernel, runtime and taints by default and draws usage, % and allocatable as one block', () => {
        const p = plan();
        expect(p.cols.map(c => c.key)).toEqual(['name', 'status', 'roles', 'version', 'internalIP', 'usage', 'pods', 'createdAt']);
        const b = p.cols.find(c => c.kind === 'usage')!;
        expect(b.subs!.map(x => x.key)).toEqual(['cpuUse', 'cpuPctAlloc', 'cpu', 'memUse', 'memPctAlloc', 'memory']);
        // the raw usage columns are shown, so the short labels apply: CPU, %, alloc | MEM, %, alloc
        expect(b.subs!.map(x => x.text)).toEqual(['CPU', '%', 'alloc', 'MEM', '%', 'alloc']);
        expect(b.subs!.map(x => x.suffix)).toEqual(['m', '', '', 'Mi', '', '']);
        expect(b.width).toBe(310);
        expect(b.subGrid).toBe('44px 36px 52px 56px 36px 56px');
    });

    it('colors % of allocatable by the node rule and prints node fractions with a decimal', () => {
        const subs = plan().cols.find(c => c.kind === 'usage')!.subs!;
        expect(subs.map(x => [x.key, x.tone, x.pctNode])).toEqual([
            ['cpuUse', null, false],
            ['cpuPctAlloc', 'node', true],
            ['cpu', null, false],
            ['memUse', null, false],
            ['memPctAlloc', 'node', true],
            ['memory', null, false]
        ]);
        expect(subs.find(x => x.key === 'cpuUse')!.res).toBe('cpu');
        expect(subs.find(x => x.key === 'memory')!.res).toBe('mem');
    });

    it('allocatable stays when metrics-server is missing; usage and its percentages drop', () => {
        const p = plan({ metricsOff: true });
        expect(p.cols.find(c => c.kind === 'usage')!.subs!.map(x => x.key)).toEqual(['cpu', 'memory']);
        expect(p.hasMetrics).toBe(false);
    });

    it('the Columns menu offers the hidden ones and a saved resize does not reveal them', () => {
        expect(plan().all.map(c => c.key)).toContain('taints');
        expect(plan({ layout: { widths: { name: 260 }, hidden: [] } }).cols.map(c => c.key)).not.toContain('taints');
        expect(plan({ layout: { shown: ['kernel'] } }).cols.map(c => c.key)).toContain('kernel');
    });

    it('gives the name column 171 px at 1440 px with the sidebar pinned (content 1166), one cluster', () => {
        const p = plan();
        const fixed = 15 + 24 + p.cols.filter(c => !c.grow).reduce((a, c) => a + c.width, 0);
        expect(1166 - fixed - 12 * p.cols.length).toBe(171);
    });

    it('exposes the allocatable indexes for the tooltip', () => {
        const u = plan().usage!;
        expect([u.cpuUse, u.cpuPctAlloc, u.cpuAlloc, u.memUse, u.memPctAlloc, u.memAlloc]).toEqual([11, 12, 13, 14, 15, 16]);
        expect(u.cpuPctNode).toBe(-1);
    });
});

// The VerticalPodAutoscaler list: no cpuUse/memUse, so no tooltip index; target/lower/upper per resource, all raw or short.
const VPAS: Column[] = [
    { key: 'cluster', type: 'dict', label: 'Cluster', width: 110 },
    { key: 'name', type: 'str', label: 'Name', width: 320 },
    { key: 'namespace', type: 'dict', label: 'Namespace', width: 120 },
    { key: 'target', type: 'str', label: 'Target', width: 160 },
    { key: 'mode', type: 'dict', label: 'Mode', width: 90 },
    { key: 'status', type: 'dict', label: 'Status', width: 120 },
    { key: 'statusTone', type: 'tone', label: 'Tone', width: 40 },
    u('cpuTarget', 'CPU', 'millicores'),
    u('cpuLower', 'CPU lower', 'millicores', { short: 'low' }),
    u('cpuUpper', 'CPU upper', 'millicores', { short: 'up' }),
    u('memTarget', 'MEM', 'MiB'),
    u('memLower', 'MEM lower', 'MiB', { short: 'low' }),
    u('memUpper', 'MEM upper', 'MiB', { short: 'up' }),
    { key: 'createdAt', type: 'time', label: 'Age', width: 56, align: 'right' }
];

describe('planColumns: the VerticalPodAutoscaler list', () => {
    const p = planColumns(VPAS, { gvr: 'autoscaling.k8s.io/v1/verticalpodautoscalers', showCluster: false });

    it('draws the six unit columns as one block with short labels, and no usage tooltip or polling', () => {
        expect(p.cols.map(c => c.key)).toEqual(['name', 'namespace', 'target', 'mode', 'status', 'usage', 'createdAt']);
        const b = p.cols.find(c => c.kind === 'usage')!;
        expect(b.subs!.map(x => [x.key, x.text, x.suffix, x.res])).toEqual([
            ['cpuTarget', 'CPU', 'm', 'cpu'],
            ['cpuLower', 'low', '', 'cpu'],
            ['cpuUpper', 'up', '', 'cpu'],
            ['memTarget', 'MEM', 'Mi', 'mem'],
            ['memLower', 'low', '', 'mem'],
            ['memUpper', 'up', '', 'mem']
        ]);
        expect(b.subs!.filter(x => x.lead).map(x => x.key)).toEqual(['memTarget']);
        expect(b.width).toBe(44 * 3 + 48 * 3 + 5 * 6 + (14 - 6));
        expect(p.usage).toBeNull();
        expect(p.hasMetrics).toBe(false);
        expect(p.metricKeys).toEqual([]);
    });

    it('keeps the status tag on the status column', () => {
        expect(p.cols.find(c => c.key === 'status')!.kind).toBe('status');
    });
});

describe('planColumns: pods vpa column', () => {
    it('is a vpa cell only on pods', () => {
        expect(planColumns(PODS, { gvr: 'v1/pods', showCluster: false }).cols.find(c => c.key === 'vpa')).toMatchObject({ kind: 'vpa', width: 36 });
        expect(planColumns(PODS, { gvr: 'x', showCluster: false }).cols.find(c => c.key === 'vpa')!.kind).toBe('text');
    });

    it('is dropped and listed disabled when no active cluster has VPA-managed pods', () => {
        const p = planColumns(PODS, { gvr: 'v1/pods', showCluster: false, vpaOff: true });
        expect(p.cols.some(c => c.key === 'vpa')).toBe(false);
        expect(p.all.find(c => c.key === 'vpa')?.note).toBe('no pods managed by VPA');
        expect(p.cols.some(c => c.key === 'usage')).toBe(true);
    });
});

describe('guessPodsUsage', () => {
    it('is on by default, off when every metrics column is hidden, on again by a sort or a shown %N column', () => {
        expect(guessPodsUsage(undefined, [])).toBe(true);
        const all = ['cpuUse', 'cpuPctReq', 'cpuPctLim', 'memUse', 'memPctReq', 'memPctLim'];
        expect(guessPodsUsage({ hidden: all }, [])).toBe(false);
        expect(guessPodsUsage({ hidden: all }, [{ col: 'cpuPctLim' }])).toBe(true);
        expect(guessPodsUsage({ hidden: all }, [{ col: 'name' }])).toBe(false);
        expect(guessPodsUsage({ hidden: all, shown: ['cpuPctNode'] }, [])).toBe(true);
    });
});

describe('guessNodesUsage', () => {
    it('is on by default, off when all four metrics columns are hidden, on again by a sort; other kinds never guess', () => {
        expect(guessUsage('v1/nodes', undefined, [])).toBe(true);
        const all = ['cpuUse', 'cpuPctAlloc', 'memUse', 'memPctAlloc'];
        expect(guessUsage('v1/nodes', { hidden: all }, [])).toBe(false);
        expect(guessUsage('v1/nodes', { hidden: all }, [{ col: 'memPctAlloc' }])).toBe(true);
        expect(guessUsage('apps/v1/deployments', undefined, [])).toBe(false);
    });
});

describe('planKey inputs', () => {
    it('two blocks of the same width with different sub-columns differ in their sub keys', () => {
        const a = planColumns(PODS, { gvr: 'v1/pods', showCluster: false, layout: { shown: ['cpuReq'], hidden: ['cpuPctReq'] } });
        const b = planColumns(PODS, { gvr: 'v1/pods', showCluster: false, layout: { shown: ['cpuLim'], hidden: ['cpuPctReq'] } });
        expect(a.grid).toBe(b.grid);
        expect(a.cols.find(c => c.subs)!.subs!.map(u => u.key)).not.toEqual(b.cols.find(c => c.subs)!.subs!.map(u => u.key));
    });
});
