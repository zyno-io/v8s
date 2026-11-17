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
