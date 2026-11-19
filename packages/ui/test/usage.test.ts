import type { Row } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import type { UsageIdx } from '../src/lib/columns.ts';

import { formatUsage, usageBar, usageTone } from '../src/lib/format.ts';
import { cpuText, memText, usageCardLine, usageTitle } from '../src/lib/usage.ts';

const IDX: UsageIdx = {
    cpuUse: 2,
    cpuReq: 3,
    cpuLim: 4,
    cpuPctReq: 5,
    cpuPctLim: 6,
    cpuPctNode: 7,
    memUse: 8,
    memReq: 9,
    memLim: 10,
    memPctReq: 11,
    memPctLim: 12,
    memPctNode: 13,
    node: 14,
    cpuPctAlloc: -1,
    cpuAlloc: -1,
    memPctAlloc: -1,
    memAlloc: -1
};
const row = (v: Partial<Record<keyof UsageIdx, unknown>>, idx: UsageIdx = IDX): Row => {
    const r: unknown[] = Array.from({ length: 15 }, () => null);
    for (const [k, x] of Object.entries(v)) r[idx[k as keyof UsageIdx]] = x;
    return r as unknown as Row;
};
// The nodes schema: usage, % of allocatable, allocatable (`cpu`, `memory`) per resource.
const NIDX: UsageIdx = {
    ...IDX,
    cpuUse: 2,
    cpuPctAlloc: 3,
    cpuAlloc: 4,
    memUse: 5,
    memPctAlloc: 6,
    memAlloc: 7,
    cpuReq: -1,
    cpuLim: -1,
    cpuPctReq: -1,
    cpuPctLim: -1,
    cpuPctNode: -1,
    memReq: -1,
    memLim: -1,
    memPctReq: -1,
    memPctLim: -1,
    memPctNode: -1,
    node: -1
};

describe('formatUsage', () => {
    it('renders no data as a dash and n/a as n/a', () => {
        expect(formatUsage('millicores', false, null)).toBe('—');
        expect(formatUsage('permille', false, undefined)).toBe('—');
        expect(formatUsage('permille', false, -1)).toBe('n/a');
        expect(formatUsage('MiB', false, -1)).toBe('n/a');
    });
    it('prints millicores and MiB as plain integers (no separators, no unit)', () => {
        expect(formatUsage('millicores', false, 1250)).toBe('1250');
        expect(formatUsage('MiB', false, 16384)).toBe('16384');
        expect(formatUsage('millicores', false, 0)).toBe('0');
    });
    it('rounds per-mille to whole percent, with one decimal for a node fraction below 10 %', () => {
        expect(formatUsage('permille', false, 324)).toBe('32');
        expect(formatUsage('permille', false, 995)).toBe('100');
        expect(formatUsage('permille', false, 4)).toBe('0');
        expect(formatUsage('permille', true, 4)).toBe('0.4');
        expect(formatUsage('permille', true, 32)).toBe('3.2');
        expect(formatUsage('permille', true, 99)).toBe('9.9');
        expect(formatUsage('permille', true, 100)).toBe('10');
        expect(formatUsage('permille', true, 456)).toBe('46');
    });
});

describe('usageTone', () => {
    it('limit: warning from 90 %, danger from 100 %', () => {
        expect(usageTone('lim', 899)).toBe('');
        expect(usageTone('lim', 900)).toBe('warning');
        expect(usageTone('lim', 999)).toBe('warning');
        expect(usageTone('lim', 1000)).toBe('danger');
        expect(usageTone('lim', 1500)).toBe('danger');
    });
    it('node: warning from 90 % only; requests are never coloured; missing values are not coloured', () => {
        expect(usageTone('node', 950)).toBe('warning');
        expect(usageTone('node', 1200)).toBe('warning');
        expect(usageTone(null, 5000)).toBe('');
        expect(usageTone('lim', null)).toBe('');
        expect(usageTone('lim', -1)).toBe('');
    });
});

describe('usageTitle', () => {
    it('says used, requested and limit with percentages, and the share of the node', () => {
        const t = usageTitle(
            IDX,
            row({
                cpuUse: 16,
                cpuReq: 50,
                cpuLim: 250,
                cpuPctReq: 320,
                cpuPctLim: 64,
                cpuPctNode: 4,
                memUse: 163,
                memReq: 128,
                memLim: 256,
                memPctReq: 1273,
                memPctLim: 637,
                memPctNode: 3,
                node: 'node-12'
            })
        );
        expect(t.split('\n')).toEqual([
            'CPU 16m used · 50m requested (32%) · 250m limit (6.4%) · 0.4% of node-12 allocatable',
            'MEM 163Mi used · 128Mi requested (127%) · 256Mi limit (64%) · 0.3% of node-12 allocatable'
        ]);
        expect(t).toContain('requested');
    });
    it('says no request / no limit / no data, and skips what is n/a', () => {
        const t = usageTitle(IDX, row({ cpuUse: 5, cpuPctReq: -1, cpuPctLim: -1, memReq: 64, memPctReq: null, node: 'n1' }));
        expect(t.split('\n')).toEqual(['CPU 5m used · no request · no limit', 'MEM no data · 64Mi requested · no limit']);
    });
    it('adds cores or GiB from 1000m / 1024Mi', () => {
        expect(cpuText(999)).toBe('999m');
        expect(cpuText(1250)).toBe('1250m (1.25 cores)');
        expect(memText(1023)).toBe('1023Mi');
        expect(memText(1536)).toBe('1536Mi (1.5Gi)');
    });
});

describe('usageCardLine', () => {
    it('is null until both values arrive', () => {
        expect(usageCardLine(IDX, row({ cpuUse: 1 }))).toBeNull();
        expect(usageCardLine(IDX, row({}))).toBeNull();
    });
    it('reads "cpu 16m · mem 163Mi" and takes the worse limit tone', () => {
        expect(usageCardLine(IDX, row({ cpuUse: 16, memUse: 163 }))).toEqual({ text: 'cpu 16m · mem 163Mi', tone: '' });
        expect(usageCardLine(IDX, row({ cpuUse: 16, memUse: 163, cpuPctLim: 920 }))!.tone).toBe('warning');
        expect(usageCardLine(IDX, row({ cpuUse: 16, memUse: 163, cpuPctLim: 920, memPctLim: 1010 }))!.tone).toBe('danger');
    });
});

describe('nodes: usage against allocatable', () => {
    it('the tooltip says used and the share of allocatable, with cores and GiB from 1000m / 1024Mi', () => {
        const t = usageTitle(NIDX, row({ cpuUse: 3920, cpuPctAlloc: 247, cpuAlloc: 15890, memUse: 20480, memPctAlloc: 317, memAlloc: 64512 }, NIDX));
        expect(t.split('\n')).toEqual([
            'CPU 3920m (3.92 cores) used · 25% of 15890m (15.89 cores) allocatable',
            'MEM 20480Mi (20Gi) used · 32% of 64512Mi (63Gi) allocatable'
        ]);
    });
    it('says no data / no allocatable', () => {
        const t = usageTitle(NIDX, row({ cpuPctAlloc: -1 }, NIDX));
        expect(t.split('\n')).toEqual(['CPU no data · no allocatable', 'MEM no data · no allocatable']);
    });
    it('the phone line is coloured by the node rule (warning from 90 %, never danger)', () => {
        expect(usageCardLine(NIDX, row({ cpuUse: 3920, memUse: 20480, cpuPctAlloc: 247, memPctAlloc: 317 }, NIDX))).toEqual({
            text: 'cpu 3920m · mem 20480Mi',
            tone: ''
        });
        expect(usageCardLine(NIDX, row({ cpuUse: 1, memUse: 1, cpuPctAlloc: 1200, memPctAlloc: 10 }, NIDX))!.tone).toBe('warning');
    });
});

describe('usageBar', () => {
    it('draws no bar without a value', () => {
        expect(usageBar('lim', null)).toBeNull();
        expect(usageBar('lim', undefined)).toBeNull();
        expect(usageBar('node', -1)).toBeNull();
    });
    it('tiers limit and node percentages at 70 and 90 %', () => {
        expect(usageBar('lim', 0)).toEqual({ tier: 'ok', fill: 0 });
        expect(usageBar('lim', 699)).toEqual({ tier: 'ok', fill: 69.9 });
        expect(usageBar('lim', 700)).toEqual({ tier: 'warn', fill: 70 });
        expect(usageBar('node', 899)?.tier).toBe('warn');
        expect(usageBar('node', 900)?.tier).toBe('hot');
    });
    it('keeps requests neutral and caps the fill at a full row', () => {
        expect(usageBar(null, 2500)).toEqual({ tier: 'n', fill: 100 });
        expect(usageBar('lim', 1300)).toEqual({ tier: 'hot', fill: 100 });
    });
});
