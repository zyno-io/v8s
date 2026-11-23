import { describe, expect, it } from 'vitest';

import type { CommandContext } from '../src/lib/command-parser.ts';

import { complete, findKind, parseCommand } from '../src/lib/command-parser.ts';
import { matchScore, rank } from '../src/lib/fuzzy.ts';

const ctx: CommandContext = {
    kinds: [
        {
            gvr: 'v1/pods',
            kind: 'Pod',
            plural: 'pods',
            shortNames: ['po'],
            namespaced: true,
            available: true,
            group: '',
            version: 'v1',
            curated: true,
            ips: true,
            clusters: []
        },
        {
            gvr: 'apps/v1/deployments',
            kind: 'Deployment',
            plural: 'deployments',
            shortNames: ['deploy'],
            namespaced: true,
            available: false,
            group: '',
            version: 'v1',
            curated: true,
            clusters: []
        },
        {
            gvr: 'v1/nodes',
            kind: 'Node',
            plural: 'nodes',
            shortNames: ['no'],
            namespaced: false,
            available: false,
            group: '',
            version: 'v1',
            curated: true,
            clusters: []
        },
        {
            gvr: 'v1/services',
            kind: 'Service',
            plural: 'services',
            shortNames: ['svc'],
            namespaced: true,
            available: false,
            group: '',
            version: 'v1',
            curated: true,
            clusters: []
        }
    ],
    namespaces: ['payments', 'search', 'kube-system'],
    clusters: [
        { id: 'prod-us', name: 'prod-us', groups: ['production'] },
        { id: 'prod-eu', name: 'prod-eu', groups: ['production', 'eu'] },
        { id: 'staging', name: 'staging', groups: [] }
    ],
    selected: ['prod-us']
};

describe('parseCommand', () => {
    it('handles empty input and quit', () => {
        expect(parseCommand('  ', ctx)).toEqual({ type: 'empty' });
        expect(parseCommand('q', ctx)).toEqual({ type: 'quit' });
        expect(parseCommand('quit', ctx)).toEqual({ type: 'quit' });
    });

    it(':ctx variants', () => {
        expect(parseCommand('ctx', ctx)).toEqual({ type: 'ctx-open' });
        expect(parseCommand('ctx prod-us staging', ctx)).toEqual({ type: 'ctx-set', ids: ['prod-us', 'staging'] });
        expect(parseCommand('ctx production', ctx)).toEqual({ type: 'ctx-set', ids: ['prod-us', 'prod-eu'] });
        expect(parseCommand('ctx +staging', ctx)).toEqual({ type: 'ctx-delta', add: ['staging'], remove: [] });
        expect(parseCommand('ctx -prod-us +prod-eu', ctx)).toEqual({ type: 'ctx-delta', add: ['prod-eu'], remove: ['prod-us'] });
        expect(parseCommand('ctx nope', ctx)).toEqual({ type: 'error', message: 'unknown cluster "nope"' });
        expect(parseCommand('ctx +prod-us staging', ctx).type).toBe('error');
    });

    it('opens kinds by plural, kind and short name', () => {
        for (const t of ['pods', 'po', 'Pod', 'PO']) expect(parseCommand(t, ctx)).toMatchObject({ type: 'open-kind', gvr: 'v1/pods' });
    });

    it('accepts a namespace and ns/name', () => {
        expect(parseCommand('po payments', ctx)).toEqual({ type: 'open-kind', gvr: 'v1/pods', plural: 'pods', namespace: 'payments' });
        expect(parseCommand('po payments/api-1', ctx)).toEqual({
            type: 'open-kind',
            gvr: 'v1/pods',
            plural: 'pods',
            namespace: 'payments',
            name: 'api-1'
        });
        expect(parseCommand('po all', ctx)).toEqual({ type: 'open-kind', gvr: 'v1/pods', plural: 'pods' });
        expect(parseCommand('po /x', ctx).type).toBe('error');
    });

    it('says unavailable kinds are not available yet', () => {
        expect(parseCommand('deploy', ctx)).toEqual({ type: 'error', message: 'Deployment (deployments) is not available yet' });
    });

    it('reports unknown commands inline and never navigates', () => {
        expect(parseCommand('bogus', ctx)).toEqual({ type: 'error', message: 'unknown command "bogus"' });
    });

    it('accepts unique prefixes, rejects ambiguous ones', () => {
        expect(parseCommand('dep', ctx)).toMatchObject({ type: 'error' }); // unique, but unavailable
        expect(findKind('dep', ctx.kinds)).toMatchObject({ kind: { plural: 'deployments' } });
        expect(findKind('s', ctx.kinds)).toMatchObject({ kind: { plural: 'services' } });
        expect(findKind('n', ctx.kinds)).toMatchObject({ kind: { plural: 'nodes' } });
        const amb = findKind(
            'p',
            {
                ...ctx,
                kinds: [
                    ...ctx.kinds,
                    {
                        gvr: 'v1/pv',
                        kind: 'PersistentVolume',
                        plural: 'persistentvolumes',
                        shortNames: ['pv'],
                        namespaced: false,
                        available: false,
                        group: '',
                        version: 'v1',
                        curated: true,
                        clusters: []
                    }
                ]
            }.kinds
        );
        expect(amb && 'ambiguous' in amb).toBe(true);
    });

    it(':ns and problems', () => {
        expect(parseCommand('ns payments', ctx)).toEqual({ type: 'ns', namespace: 'payments' });
        expect(parseCommand('ns all', ctx)).toEqual({ type: 'ns', namespace: null });
        expect(parseCommand('ns a b', ctx).type).toBe('error');
    });
    it(':ns +name / -name add to and remove from the filter', () => {
        expect(parseCommand('ns +payments', ctx)).toEqual({ type: 'ns-delta', add: ['payments'], remove: [] });
        expect(parseCommand('ns +a -b +c', ctx)).toEqual({ type: 'ns-delta', add: ['a', 'c'], remove: ['b'] });
        expect(parseCommand('namespace -kube-system', ctx)).toEqual({ type: 'ns-delta', add: [], remove: ['kube-system'] });
        expect(parseCommand('ns a +b', ctx).type).toBe('error');
        expect(parseCommand('ns +', ctx).type).toBe('error');
    });
    it(':ns completes +name from namespaces outside the filter and -name from the filter', () => {
        const c: CommandContext = { ...ctx, namespaces: ['payments', 'platform', 'shop'], filter: ['shop'] };
        expect(complete('ns +p', c).map(x => x.label)).toEqual(['+payments', '+platform']);
        expect(complete('ns +', c).map(x => x.label)).not.toContain('+shop');
        expect(complete('ns -', c)).toEqual([{ label: '-shop', detail: 'in the filter', insert: 'ns -shop' }]);
        expect(complete('ns +payments +', c).map(x => x.label)).toEqual(['+platform']);
    });
    it('bare :ns and :namespace open the Namespaces list', () => {
        const withNs: CommandContext = {
            ...ctx,
            kinds: [
                ...ctx.kinds,
                {
                    gvr: 'v1/namespaces',
                    kind: 'Namespace',
                    plural: 'namespaces',
                    shortNames: ['ns'],
                    namespaced: false,
                    available: true,
                    group: '',
                    version: 'v1',
                    curated: true,
                    clusters: []
                }
            ]
        };
        const open = { type: 'open-kind', gvr: 'v1/namespaces', plural: 'namespaces' };
        expect(parseCommand('ns', withNs)).toEqual(open);
        expect(parseCommand('namespace', withNs)).toEqual(open);
        expect(parseCommand('ns payments', withNs)).toEqual({ type: 'ns', namespace: 'payments' });
        expect(parseCommand('ns', ctx).type).toBe('error'); // no Namespaces kind served
        // `ns ` (trailing space) is choosing a namespace: not the bare command
        expect(parseCommand('ns ', withNs).type).toBe('error');
        expect(parseCommand('namespace ', withNs).type).toBe('error');
        expect(parseCommand('pb', ctx)).toEqual({ type: 'problems' });
        expect(parseCommand('problems', ctx)).toEqual({ type: 'problems' });
    });

    it('parses star and unstar', () => {
        expect(parseCommand('star', ctx)).toEqual({ type: 'star', on: true });
        expect(parseCommand('unstar', ctx)).toEqual({ type: 'star', on: false });
    });

    it('treats an argument on cluster-scoped kinds as an object name', () => {
        const nodes = {
            ...ctx,
            kinds: ctx.kinds.map(k => (k.plural === 'nodes' ? { ...k, available: true, group: '', version: 'v1', curated: true, clusters: [] } : k))
        };
        expect(parseCommand('nodes x', nodes)).toEqual({ type: 'open-kind', gvr: 'v1/nodes', plural: 'nodes', name: 'x' });
        expect(complete('nodes ', nodes)).toEqual([]);
        expect(parseCommand('nodes', nodes)).toMatchObject({ type: 'open-kind', gvr: 'v1/nodes' });
    });
});

describe('IP commands', () => {
    it(':ip opens the palette search', () => {
        expect(parseCommand('ip 10.0.0.1', ctx)).toEqual({ type: 'ip', text: '10.0.0.1' });
        expect(parseCommand('ip 10.0.0.1 - 10.0.0.9', ctx)).toEqual({ type: 'ip', text: '10.0.0.1 - 10.0.0.9' });
        expect(parseCommand('ip', ctx).type).toBe('error');
    });
    it(':<type> <ip> filters by address on kinds with IPs', () => {
        expect(parseCommand('po 10.42.', ctx)).toEqual({ type: 'open-kind', gvr: 'v1/pods', plural: 'pods', text: '10.42.' });
        expect(parseCommand('pods 10.0.0.0/16', ctx)).toMatchObject({ type: 'open-kind', text: '10.0.0.0/16' });
    });
    it(':<type> <namespace> is unchanged', () => {
        expect(parseCommand('po payments', ctx)).toEqual({ type: 'open-kind', gvr: 'v1/pods', plural: 'pods', namespace: 'payments' });
        expect(parseCommand('po payments/api-1', ctx)).toMatchObject({ namespace: 'payments', name: 'api-1' });
    });
    it('completes no namespaces for an IP argument, and lists :ip', () => {
        expect(complete('po 10.4', ctx)).toEqual([]);
        expect(complete('ip ', ctx)).toEqual([]);
        expect(complete('i', ctx).map(c => c.label)).toContain('ip');
    });
});

describe('complete', () => {
    it('lists commands and kinds for an empty input', () => {
        const labels = complete('', ctx, 50).map(c => c.label);
        expect(labels).toEqual(expect.arrayContaining(['ctx', 'ns', 'pb', 'q', 'star', 'unstar', 'pods', 'po', 'deploy']));
    });

    it('ranks prefix matches before fuzzy matches', () => {
        const out = complete('po', ctx).map(c => c.label);
        expect(out[0]).toBe('po');
        expect(out).toContain('pods');
        const fuzzy = complete('dpl', ctx).map(c => c.label);
        expect(fuzzy).toContain('deployments');
        const mixed = complete('s', ctx).map(c => c.label);
        expect(mixed.indexOf('svc')).toBeLessThan(mixed.indexOf('deploy') === -1 ? 99 : mixed.indexOf('deploy'));
    });

    it('marks unavailable kinds', () => {
        expect(complete('deploy', ctx).find(c => c.label === 'deploy')?.detail).toMatch(/not available yet/);
    });

    it('completes clusters and groups after :ctx, keeping +/- and skipping used ones', () => {
        const out = complete('ctx pr', ctx).map(c => c.insert);
        expect(out).toEqual(expect.arrayContaining(['ctx prod-us', 'ctx prod-eu', 'ctx production']));
        expect(complete('ctx +st', ctx)[0].insert).toBe('ctx +staging');
        expect(complete('ctx prod-us pr', ctx).map(c => c.label)).not.toContain('prod-us');
        expect(complete('ctx ', ctx).length).toBeGreaterThan(0);
    });

    it('completes namespaces after a kind and :ns', () => {
        expect(complete('po pay', ctx)[0]).toMatchObject({ label: 'payments', insert: 'po payments' });
        expect(complete('ns ', ctx).map(c => c.label)).toContain('all');
        expect(complete('ns kube', ctx)[0].label).toBe('kube-system');
        expect(complete('po payments/', ctx)).toEqual([]);
    });

    it('returns nothing for unknown heads with args', () => {
        expect(complete('bogus x', ctx)).toEqual([]);
    });
});

describe('fuzzy', () => {
    it('scores prefix < fuzzy < no match', () => {
        expect(matchScore('po', 'pods')).toBeLessThan(100);
        expect(matchScore('pds', 'pods')).toBeGreaterThanOrEqual(100);
        expect(matchScore('zz', 'pods')).toBe(-1);
    });
    it('ranks stably', () => {
        expect(rank('a', ['ab', 'a', 'ba'], s => s)).toEqual(['a', 'ab', 'ba']);
    });
});
