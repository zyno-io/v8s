import type { DropGroup, HubbleStatus } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import {
    DROP_WINDOWS,
    dropKey,
    fillPct,
    filterDrops,
    formatUptime,
    policyRef,
    sortDrops,
    statusHints,
    totalDrops,
    workloadRef
} from '../src/lib/network.ts';

const g = (o: Partial<Omit<DropGroup, 'src' | 'dst'>> & { src?: string; dst?: string }): DropGroup => {
    const { src, dst, ...rest } = o;
    return {
        cluster: 'c1',
        src: { namespace: 'shop', workload: src ?? 'web' },
        dst: { namespace: 'shop', workload: dst ?? 'db' },
        reason: 'POLICY_DENIED',
        count: 1,
        lastSeen: 1000,
        policies: [],
        ...rest
    };
};

describe('drops', () => {
    const groups = [g({ src: 'a', count: 5, lastSeen: 10 }), g({ src: 'b', count: 50, lastSeen: 5 }), g({ src: 'c', count: 5, lastSeen: 20 })];
    it('sorts by count then recency, or by recency', () => {
        expect(sortDrops(groups, 'count').map(x => x.src.workload)).toEqual(['b', 'c', 'a']);
        expect(sortDrops(groups, 'lastSeen').map(x => x.src.workload)).toEqual(['c', 'a', 'b']);
    });
    it('does not mutate its input', () => {
        const copy = [...groups];
        sortDrops(groups, 'count');
        expect(groups).toEqual(copy);
    });
    it('filters on every term over names, cluster, reason and policies', () => {
        expect(filterDrops(groups, '')).toHaveLength(3);
        expect(filterDrops(groups, 'shop/b')).toHaveLength(1);
        expect(filterDrops(groups, 'policy_denied db')).toHaveLength(3);
        expect(filterDrops([g({ policies: [{ kind: 'NetworkPolicy', name: 'deny-all' }] })], 'deny-all')).toHaveLength(1);
        expect(filterDrops(groups, 'nomatch')).toHaveLength(0);
    });
    it('keys and totals', () => {
        expect(dropKey(groups[0]!)).toBe('c1|shop/a|shop/db|POLICY_DENIED');
        expect(totalDrops(groups)).toBe(60);
    });
    it('offers only windows the agent allows', () => {
        expect(Math.max(...DROP_WINDOWS.map(w => w.sec))).toBeLessThanOrEqual(3600);
    });
});

describe('policy links', () => {
    const kinds = [
        { gvr: 'networking.k8s.io/v1/networkpolicies', clusters: ['c1'] },
        { gvr: 'cilium.io/v2/ciliumnetworkpolicies', clusters: ['c2'] }
    ];
    it('resolves served kinds only', () => {
        expect(policyRef('c1', { kind: 'NetworkPolicy', namespace: 'shop', name: 'p' }, kinds)).toEqual({
            cluster: 'c1',
            gvr: 'networking.k8s.io/v1/networkpolicies',
            namespace: 'shop',
            name: 'p'
        });
        expect(policyRef('c1', { kind: 'CiliumNetworkPolicy', namespace: 'shop', name: 'p' }, kinds)).toBeNull();
        expect(policyRef('c1', { kind: 'CiliumClusterwideNetworkPolicy', name: 'p' }, kinds)).toBeNull();
        expect(policyRef('c1', { kind: 'Other', name: 'p' }, kinds)).toBeNull();
    });
    it('workload refs need a namespace', () => {
        expect(workloadRef('c1', { namespace: 'shop', workload: 'web' })).toMatchObject({ gvr: 'apps/v1/deployments', name: 'web' });
        expect(workloadRef('c1', { workload: 'world' })).toBeNull();
    });
});

describe('status', () => {
    const s = (o: Partial<HubbleStatus>): HubbleStatus =>
        ({
            cluster: 'c',
            available: true,
            numFlows: 50,
            maxFlows: 100,
            seenFlows: 10,
            flowsPerSec: 1,
            uptimeSec: 90,
            connectedNodes: 3,
            unavailableNodes: 0,
            nodes: [],
            ...o
        }) as HubbleStatus;
    it('buffer fill', () => {
        expect(fillPct(s({}))).toBe(50);
        expect(fillPct(s({ maxFlows: 0 }))).toBe(0);
        expect(fillPct(s({ numFlows: 500 }))).toBe(100);
    });
    it('hints', () => {
        expect(statusHints(s({ maxFlows: 100000 }))).toEqual([]);
        expect(statusHints(s({ maxFlows: 100000, unavailableNodes: 2 }))[0]).toMatch(/2 nodes are unavailable/);
        expect(statusHints(s({ maxFlows: 300 })).join(' ')).toMatch(/buffer is small/);
        expect(statusHints(s({ maxFlows: 100000, seenFlows: 0 })).join(' ')).toMatch(/no flows yet/);
    });
    it('uptime', () => {
        expect(formatUptime(90)).toBe('1m 30s');
        expect(formatUptime(3700)).toBe('1h 1m');
        expect(formatUptime(90000)).toBe('1d 1h');
    });
});
