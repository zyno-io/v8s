import type { IPSearch } from '@v8s/client';

import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

import { ipItems, kindPlural, looksLikeIP, pendingText, skippedText } from '../src/lib/ip-search.ts';

// The same vectors the Go parser is tested against (agent/internal/ipx/testdata/detect.json).
const vectors: { in: string; ip: boolean; mode: string; norm: string }[] = JSON.parse(
    readFileSync(new URL('../../../agent/internal/ipx/testdata/detect.json', import.meta.url), 'utf8')
);

describe('looksLikeIP', () => {
    it('has the shared vectors', () => expect(vectors.length).toBeGreaterThan(30));
    for (const v of vectors) {
        it(`${JSON.stringify(v.in)} -> ${v.ip}`, () => expect(looksLikeIP(v.in)).toBe(v.ip));
    }
    it('rejects long input', () => expect(looksLikeIP('1'.repeat(101))).toBe(false));
});

const ip: IPSearch = {
    query: '10.96.0.',
    mode: 'prefix',
    groups: [
        {
            gvr: 'v1/services',
            kind: 'Service',
            total: 250,
            hits: [
                {
                    cluster: 'prod-us',
                    gvr: 'v1/services',
                    namespace: 'team-0',
                    name: 'svc-0',
                    field: 'spec.clusterIPs',
                    label: 'Cluster IP',
                    value: '10.96.0.1',
                    match: 'prefix'
                }
            ]
        },
        {
            gvr: 'v1/nodes',
            kind: 'Node',
            total: 1,
            hits: [
                {
                    cluster: 'prod-us',
                    gvr: 'v1/nodes',
                    name: 'node-0',
                    field: 'spec.podCIDRs',
                    label: 'Pod CIDR',
                    value: '10.96.0.0/23',
                    match: 'contains'
                }
            ]
        },
        {
            gvr: 'v1/pods',
            kind: 'Pod',
            total: 1,
            hits: [
                {
                    cluster: 'prod-eu',
                    gvr: 'v1/pods',
                    namespace: 'a',
                    name: 'p',
                    field: 'status.podIPs',
                    label: 'Pod IP',
                    value: '10.96.0.9',
                    match: 'exact',
                    detail: 'svc'
                }
            ]
        }
    ],
    pending: [{ cluster: 'prod-us', kind: 'Ingress' }],
    skipped: [{ cluster: 'prod-eu', kind: 'Gateway', reason: 'forbidden' }]
};

describe('ipItems', () => {
    const items = ipItems(ip);
    it('puts a section per kind with its true total', () => {
        expect([...new Set(items.map(i => i.section))]).toEqual(['IP · Services (250)', 'IP · Nodes (1)', 'IP · Pods (1)']);
    });
    it('adds "Show all" only when the total exceeds the hits', () => {
        const all = items.filter(i => i.testid === 'palette-ip-showall');
        expect(all).toHaveLength(1);
        expect(all[0].label).toBe('Show all 250 services matching 10.96.0.');
        expect(all[0].action).toEqual({ type: 'list', gvr: 'v1/services', text: '10.96.0.' });
    });
    it('words the match and builds the open action', () => {
        const hits = items.filter(i => i.testid === 'palette-ip-hit');
        expect(hits.map(h => h.detail)).toEqual(['Cluster IP · team-0', 'Pod CIDR · contains', 'Pod IP · a · svc']);
        expect(hits[0].action).toEqual({ type: 'open', ref: { cluster: 'prod-us', gvr: 'v1/services', namespace: 'team-0', name: 'svc-0' } });
        expect(hits.map(h => h.strong)).toEqual([false, false, true]);
        expect(hits[0].cluster).toBe('prod-us');
        expect(hits[1].value).toBe('10.96.0.0/23');
    });
    it('is empty without groups', () => expect(ipItems({ query: 'x', mode: 'addr', groups: [] })).toEqual([]));
});

describe('pending and skipped text', () => {
    it('names what is loading and what was skipped', () => {
        expect(pendingText(ip)).toBe('Still loading one source…');
        expect(skippedText(ip)).toBe('Not searched: Gateway on prod-eu (forbidden)');
        const more: IPSearch = {
            ...ip,
            skipped: [
                { cluster: 'a', kind: 'Pool', reason: 'budget' },
                { cluster: 'b', kind: 'NAD', reason: 'error' }
            ]
        };
        expect(skippedText(more)).toBe('Not searched: Pool on a (watch limit reached), NAD on b (could not start)');
        expect(pendingText(undefined)).toBe('');
        expect(skippedText({ query: '', mode: 'addr', groups: [] })).toBe('');
    });
});

describe('kindPlural', () => {
    it('splits CamelCase and keeps acronyms', () => {
        expect(kindPlural('IPAddressPool')).toBe('IP address pools');
        expect(kindPlural('EndpointSlice')).toBe('endpoint slices');
        expect(kindPlural('ServiceCIDR')).toBe('service CIDRs');
        expect(kindPlural('BGPPeer')).toBe('BGP peers');
        expect(kindPlural('NetworkAttachmentDefinition')).toBe('network attachment definitions');
        expect(kindPlural('CiliumLoadBalancerIPPool')).toBe('cilium load balancer IP pools');
        expect(kindPlural('Ingress')).toBe('ingresses');
        expect(kindPlural('Pod')).toBe('pods');
        expect(kindPlural('NetworkPolicy')).toBe('network policies');
    });
});
