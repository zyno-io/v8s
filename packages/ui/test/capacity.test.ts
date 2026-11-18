import type { Column, Row } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import { capacityRow, capacityView, usageNote } from '../src/lib/capacity.ts';

const KEYS = [
    'nodes',
    'nodesReporting',
    'cpuAlloc',
    'memAlloc',
    'cpuReq',
    'memReq',
    'cpuLim',
    'memLim',
    'cpuUse',
    'memUse',
    'podsCounted',
    'podsNoCPULim',
    'podsNoMemLim'
];
const COLS: Column[] = [
    { key: 'cluster', type: 'dict', label: 'Cluster' },
    { key: 'name', type: 'str', label: 'Name' },
    ...KEYS.map(key => ({ key, type: 'int' as const, label: key }))
];

const BASE = {
    nodes: 150,
    nodesReporting: 150,
    cpuAlloc: 112_000,
    memAlloc: 945 * 1024,
    cpuReq: 69_440, // 62 %
    memReq: 400 * 1024,
    cpuLim: 160_160, // 143 %
    memLim: 1000 * 1024,
    cpuUse: 41_200,
    memUse: 320 * 1024,
    podsCounted: 3000,
    podsNoCPULim: 40,
    podsNoMemLim: 12
};

describe('capacityRow', () => {
    it('maps the int cells by key and skips cluster and name', () => {
        // row = [id, ver, cluster, name, ...cells]
        const r = capacityRow(COLS, [1, 1, 'prod', 'prod', ...KEYS.map(k => (BASE as Record<string, number>)[k])] as unknown as Row)!;
        expect(r.cpuAlloc).toBe(112_000);
        expect(r.nodes).toBe(150);
        expect(r.cluster).toBeUndefined();
    });
    it('is null without a row or columns', () => {
        expect(capacityRow(COLS, undefined)).toBeNull();
        expect(capacityRow(null, [1, 1] as unknown as Row)).toBeNull();
    });
});

describe('capacityView', () => {
    it('CPU: big usage %, share of requests and limits, amount in cores', () => {
        const v = capacityView('cpu', BASE, 'ok');
        expect(v.ok).toBe(true);
        expect(v.usage).toBe(true);
        expect(v.pct).toBe('37'); // 41.2 / 112
        expect(v.tone).toBe('');
        expect(v.share).toBe('requests 62% · limits 143%');
        expect(v.amount).toBe('41.2 of 112 cores');
        expect(v.fill).toBeCloseTo(36.8, 1);
        expect(v.tick).toBeCloseTo(62, 0);
        expect(v.title).toContain('150 of 150 nodes reporting');
        expect(v.title).toContain('40 without a CPU limit');
    });
    it('memory: GiB amounts', () => {
        const v = capacityView('mem', BASE, 'ok');
        expect(v.amount).toBe('320 of 945 GiB');
        expect(v.pct).toBe('34');
        expect(v.share).toBe('requests 42% · limits 106%');
        expect(v.title).toContain('12 without a memory limit');
    });
    it('tones: warning from 90 %, danger from 100 %; the bar never overflows', () => {
        expect(capacityView('cpu', { ...BASE, cpuUse: 100_800 }, 'ok').tone).toBe('warning');
        const hot = capacityView('cpu', { ...BASE, cpuUse: 120_000 }, 'ok');
        expect(hot.tone).toBe('danger');
        expect(hot.pct).toBe('107');
        expect(hot.fill).toBe(100);
    });
    it('one decimal below 10 %', () => {
        expect(capacityView('cpu', { ...BASE, cpuUse: 3_360 }, 'ok').pct).toBe('3.0');
    });
    it('without metrics-server the big number is the requested share, and it says why', () => {
        const v = capacityView('cpu', { ...BASE, cpuUse: -1, memUse: -1, nodesReporting: 0 }, 'absent');
        expect(v.usage).toBe(false);
        expect(v.pct).toBe('62');
        expect(v.amount).toBe('69.4 of 112 cores requested');
        expect(v.note).toBe('usage: metrics-server not installed');
        expect(v.fill).toBeCloseTo(62, 0);
        expect(v.share).toBe('requests 62% · limits 143%');
    });
    it('shows nothing definite until allocatable is known', () => {
        expect(capacityView('cpu', null, 'ok')).toMatchObject({ ok: false, pct: '—' });
        expect(capacityView('mem', { ...BASE, memAlloc: 0 }, 'ok').ok).toBe(false);
    });
});

describe('usageNote', () => {
    it('explains each state; unknown and ok with reporting nodes say nothing', () => {
        expect(usageNote('absent', 0)).toMatch(/not installed/);
        expect(usageNote('forbidden', 0)).toMatch(/not allowed/);
        expect(usageNote('unavailable', 0)).toMatch(/not answering/);
        expect(usageNote('ok', 0)).toMatch(/no node reports/);
        expect(usageNote('ok', 3)).toBe('');
        expect(usageNote(undefined, 0)).toBe('');
        expect(usageNote('', 0)).toBe('');
    });
});
