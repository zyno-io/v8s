import type { Column, Row } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import { buildVpaIndex, sameVpaIndex, vpaKey, vpaWantKey } from '../src/lib/vpa-status.ts';

const cols = [
    { key: 'cluster', type: 'dict' },
    { key: 'name', type: 'str' },
    { key: 'namespace', type: 'dict' },
    { key: 'target', type: 'str' },
    { key: 'status', type: 'dict' },
    { key: 'statusTone', type: 'tone' },
    { key: 'cpuTarget', type: 'int', unit: 'millicores' }
] as Column[];
const row = (id: number, name: string, status: string, tone: number, cpu: number): Row =>
    [id, 1, 'c1', name, 'ns', 'Deployment/x', status, tone, cpu] as Row;

describe('VPA status index', () => {
    it('maps cluster/namespace/name to status and tone', () => {
        const rows = new Map([
            [1, row(1, 'api', 'Provided', 1, 100)],
            [2, row(2, 'web', 'LowConfidence', 2, 50)]
        ]);
        const ix = buildVpaIndex(cols, [1, 2], rows);
        expect(ix.get(vpaKey('c1', 'ns', 'api'))).toEqual({ status: 'Provided', tone: 'success' });
        expect(ix.get(vpaKey('c1', 'ns', 'web'))).toEqual({ status: 'LowConfidence', tone: 'warning' });
        expect(buildVpaIndex(null, [1], rows).size).toBe(0);
    });

    it('treats a recommendation change as no change (nothing re-renders)', () => {
        const a = buildVpaIndex(cols, [1], new Map([[1, row(1, 'api', 'Provided', 1, 100)]]));
        const b = buildVpaIndex(cols, [1], new Map([[1, row(1, 'api', 'Provided', 1, 250)]]));
        const c = buildVpaIndex(cols, [1], new Map([[1, row(1, 'api', 'LowConfidence', 2, 250)]]));
        expect(sameVpaIndex(a, b)).toBe(true);
        expect(sameVpaIndex(a, c)).toBe(false);
    });
});

describe('vpaWantKey', () => {
    it('round-trips cluster ids holding separators', () => {
        const k = vpaWantKey('autoscaling.k8s.io/v1/verticalpodautoscalers', ['prod|eu', 'a,b']);
        expect(JSON.parse(k)).toEqual(['autoscaling.k8s.io/v1/verticalpodautoscalers', ['prod|eu', 'a,b']]);
        expect(vpaWantKey('g', ['x'])).toBe(vpaWantKey('g', ['x']));
    });
});
