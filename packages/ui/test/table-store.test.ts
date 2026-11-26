import type { Router } from 'vue-router';

import { ProblemsGVR } from '@v8s/client';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { kinds } from '../src/lib/agent.ts';
import { runCommand } from '../src/lib/run-command.ts';
import { isFiltered, selectionKey, stateQuery, useTableStore } from '../src/stores/table.ts';

beforeEach(() => setActivePinia(createPinia()));

describe('selection', () => {
    const a = { cluster: 'c', gvr: 'v1/pods', namespace: 'ns', name: 'web-1' };
    const b = { cluster: 'c', gvr: 'v1/pods', namespace: 'ns', name: 'web-10' };

    it('is keyed by object identity, not by row id', () => {
        expect(selectionKey(a)).not.toBe(selectionKey(b));
        const t = useTableStore();
        t.select('v1/pods', a);
        expect(t.of('v1/pods').selected.has(selectionKey(a))).toBe(true);
        t.select('v1/pods', a);
        expect(t.of('v1/pods').selected.size).toBe(0);
    });

    it('resetInteraction clears selection and focus', () => {
        const t = useTableStore();
        t.select('v1/pods', a);
        t.of('v1/pods').focusId = 7;
        t.resetInteraction('v1/pods');
        expect(t.of('v1/pods').selected.size).toBe(0);
        expect(t.of('v1/pods').focusId).toBe(0);
    });
});

describe('namespace filter across kinds', () => {
    afterEach(() => {
        kinds.value = [];
    });

    it('is one selection for every namespaced kind, so switching kinds keeps it', () => {
        const t = useTableStore();
        t.namespaces = ['shop', 'pay'];
        for (const gvr of ['v1/pods', 'apps/v1/deployments', 'v1/services', 'some.crd.io/v1/widgets']) {
            expect(t.namespacesOf(gvr)).toEqual(['shop', 'pay']);
            expect(stateQuery(gvr, t.of(gvr), t.namespacesOf(gvr)).namespaces).toEqual(['shop', 'pay']);
        }
    });

    it('does not filter cluster-scoped kinds or Problems, and survives a visit to them', () => {
        kinds.value = [{ gvr: 'v1/nodes', namespaced: false } as never, { gvr: 'v1/pods', namespaced: true } as never];
        const t = useTableStore();
        t.namespaces = ['shop'];
        expect(t.namespacesOf('v1/nodes')).toEqual([]);
        expect(stateQuery('v1/nodes', t.of('v1/nodes'), t.namespacesOf('v1/nodes')).namespaces).toEqual([]);
        expect(isFiltered(t.of('v1/nodes'), t.namespacesOf('v1/nodes'))).toBe(false);
        expect(t.namespacesOf(ProblemsGVR)).toEqual([]);
        expect(t.namespacesOf('v1/pods')).toEqual(['shop']);
    });

    it('keeps its identity when set to the same list (views are not re-queried)', () => {
        const t = useTableStore();
        t.setNamespaces(['a', 'b']);
        const same = t.namespaces;
        t.setNamespaces(['a', 'b']);
        expect(t.namespaces).toBe(same);
        expect(t.namespacesOf('v1/pods')).toBe(same);
        t.setNamespaces(['a']);
        expect(t.namespaces).toEqual(['a']);
    });

    it('navigation presets clear only the kind filters; "Clear filters" also clears the namespaces', () => {
        const t = useTableStore();
        t.namespaces = ['shop'];
        t.of('v1/pods').text = 'web';
        t.of('v1/pods').tones = [3];
        t.clearKindFilters('v1/pods');
        expect(t.of('v1/pods').text).toBe('');
        expect(t.of('v1/pods').tones).toEqual([]);
        expect(t.namespaces).toEqual(['shop']);
        t.clearFilters('apps/v1/deployments');
        expect(t.namespaces).toEqual([]);
    });

    it('"Clear filters" on a cluster-scoped kind leaves the namespaces it does not show', () => {
        kinds.value = [{ gvr: 'v1/nodes', namespaced: false } as never];
        const t = useTableStore();
        t.namespaces = ['shop'];
        t.clearFilters('v1/nodes');
        expect(t.namespaces).toEqual(['shop']);
    });
});

describe('node filter (Pods)', () => {
    const router = () => ({ currentRoute: { value: { path: '/r/v1/nodes', params: {} } }, push: vi.fn(async () => {}) }) as unknown as Router;

    it('filters the pods view on its node column, beside the namespaces and tones', () => {
        const t = useTableStore();
        t.namespaces = ['shop'];
        const s = t.of('v1/pods');
        s.tones = [3];
        s.nodes = ['node-3', 'node-1'];
        const q = stateQuery('v1/pods', s, t.namespacesOf('v1/pods'));
        expect(q.namespaces).toEqual(['shop']);
        expect(q.filters).toEqual([
            { col: 'statusTone', op: 'in', value: ['3'] },
            { col: 'node', op: 'in', value: ['node-3', 'node-1'] }
        ]);
        expect(isFiltered(s, [])).toBe(true);
    });

    it('is cleared with the kind filters and by "Clear filters"; other kinds never send it', () => {
        const t = useTableStore();
        t.of('v1/pods').nodes = ['node-3'];
        t.clearKindFilters('v1/pods');
        expect(t.of('v1/pods').nodes).toEqual([]);
        t.of('v1/pods').nodes = ['node-3'];
        t.clearFilters('v1/pods');
        expect(t.of('v1/pods').nodes).toEqual([]);
        const d = t.of('apps/v1/deployments');
        d.nodes = ['node-3'];
        expect(stateQuery('apps/v1/deployments', d, []).filters).toEqual([]);
    });

    it('showPodsOnNode opens every pod on that node: node set, namespaces and Pods filters cleared', async () => {
        const { showPodsOnNode } = await import('../src/lib/node-pods.ts');
        const t = useTableStore();
        t.namespaces = ['shop'];
        const s = t.of('v1/pods');
        s.text = 'web';
        s.tones = [3];
        s.nodes = ['node-1', 'node-2'];
        const r = router();
        await showPodsOnNode(r, 'node-3');
        expect(r.push).toHaveBeenCalledWith('/pods');
        expect(s.nodes).toEqual(['node-3']);
        expect(s.text).toBe('');
        expect(s.tones).toEqual([]);
        expect(t.namespaces).toEqual([]);
    });
});

describe('commands keep the namespace filter across kinds', () => {
    const router = (path: string, params: Record<string, unknown> = {}) =>
        ({ currentRoute: { value: { path, params } }, push: vi.fn(async () => {}) }) as unknown as Router;

    beforeEach(() => {
        const mem = new Map<string, string>();
        vi.stubGlobal('localStorage', { getItem: (k: string) => mem.get(k) ?? null, setItem: (k: string, v: string) => mem.set(k, v) });
    });
    afterEach(() => vi.unstubAllGlobals());

    it(':ns on one kind applies on the next one opened with :<type>', async () => {
        const t = useTableStore();
        expect(await runCommand({ type: 'ns', namespaces: ['shop'] }, router('/pods'))).toBeNull();
        await runCommand({ type: 'open-kind', gvr: 'apps/v1/deployments', plural: 'deployments' }, router('/pods'));
        expect(t.namespacesOf('apps/v1/deployments')).toEqual(['shop']);
        await runCommand({ type: 'ns-delta', add: ['pay'], remove: [] }, router('/r/apps/v1/deployments', { gvr: ['apps', 'v1', 'deployments'] }));
        expect(t.namespacesOf('v1/pods')).toEqual(['shop', 'pay']);
    });

    it(':ns on a cluster-scoped list is refused and leaves the filter alone', async () => {
        kinds.value = [{ gvr: 'v1/nodes', namespaced: false, plural: 'nodes', kind: 'Node' } as never];
        const t = useTableStore();
        t.namespaces = ['shop'];
        const nodes = router('/r/v1/nodes', { gvr: ['v1', 'nodes'] });
        expect(await runCommand({ type: 'ns', namespaces: ['pay'] }, nodes)).toBe('Nodes are not in namespaces');
        expect(await runCommand({ type: 'ns-delta', add: ['pay'], remove: [] }, nodes)).toBe('Nodes are not in namespaces');
        expect(t.namespaces).toEqual(['shop']);
        kinds.value = [];
    });

    it(':<type> with an IP or a name keeps the filter; :<type> <ns> replaces it', async () => {
        const t = useTableStore();
        t.namespaces = ['shop'];
        await runCommand({ type: 'open-kind', gvr: 'v1/services', plural: 'services', text: '10.0.0.1' }, router('/pods'));
        expect(t.of('v1/services').text).toBe('10.0.0.1');
        expect(t.namespaces).toEqual(['shop']);
        await runCommand({ type: 'open-kind', gvr: 'v1/pods', plural: 'pods', namespaces: ['pay'] }, router('/pods'));
        expect(t.namespaces).toEqual(['pay']);
    });
});
