import type { Column, ProblemClassInfo } from '@v8s/client';

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
    actionsForClass,
    aggregateKey,
    applyPresetPatch,
    buildProblemsQuery,
    classesWithProblems,
    classPatch,
    compareFamilies,
    currentClassSet,
    familyChips,
    groupCountQuery,
    isRollup,
    makeRefresher,
    overriddenIn,
    presetDiff,
    presetsOf,
    problemCols,
    problemTotals,
    readRow,
    savePresetPatch,
    severityValues,
    snoozeUntil,
    sortGroupKeys
} from '@/lib/problems.ts';
import { mergePatch } from '@/lib/settings.ts';

const info = (p: Partial<ProblemClassInfo> & { class: string; family: string }): ProblemClassInfo => ({
    title: p.class,
    severity: 'error',
    default: true,
    enabled: true,
    counts: {},
    ...p
});

const classes: ProblemClassInfo[] = [
    info({ class: 'pod.crashloop', family: 'pods', counts: { a: { total: 5, acked: 2 }, b: { total: 3, acked: 0 } } }),
    info({ class: 'pod.pending', family: 'pods', counts: { a: { total: 1, acked: 0 } } }),
    info({ class: 'node.not-ready', family: 'nodes', counts: { b: { total: 2, acked: 1 } } }),
    info({ class: 'network.policy-drops', family: 'flows', default: false, enabled: false, counts: {} }),
    info({ class: 'flux.stale', family: 'flux', counts: { a: { total: 4, acked: 0 } }, enabledIn: { a: false } })
];

describe('familyChips', () => {
    it('sums totals minus acked over the active clusters', () => {
        const chips = familyChips(classes, ['a', 'b'], false);
        expect(chips.find(c => c.family === 'pods')?.count).toBe(3 + 3 + 1);
        expect(chips.find(c => c.family === 'nodes')?.count).toBe(1);
    });
    it('counts acknowledged ones when shown', () => {
        expect(familyChips(classes, ['a', 'b'], true).find(c => c.family === 'pods')?.count).toBe(9);
    });
    it('only counts the active clusters', () => {
        expect(familyChips(classes, ['b'], false).find(c => c.family === 'pods')?.count).toBe(3);
        expect(familyChips(classes, ['b'], false).find(c => c.family === 'flux')?.count).toBe(0);
    });
    it('marks a family whose classes are all disabled as off', () => {
        const chips = familyChips(classes, ['a', 'b'], false);
        expect(chips.find(c => c.family === 'flows')?.off).toBe(true);
        expect(chips.find(c => c.family === 'pods')?.off).toBe(false);
    });
    it('honours per-cluster overrides: disabled in the only active cluster means off', () => {
        const chips = familyChips(classes, ['a'], false);
        expect(chips.find(c => c.family === 'flux')?.off).toBe(true);
        expect(chips.find(c => c.family === 'flux')?.count).toBe(0);
        expect(familyChips(classes, ['b'], false).find(c => c.family === 'flux')?.off).toBe(false);
    });
    it('orders families by the preferred order, unknown ones last', () => {
        const order = familyChips([...classes, info({ class: 'x.y', family: 'zzz' }), info({ class: 'p.q', family: 'platform' })], ['a'], false).map(
            c => c.family
        );
        expect(order).toEqual(['pods', 'nodes', 'flux', 'flows', 'platform', 'zzz']);
        expect(compareFamilies('pods', 'workloads')).toBeLessThan(0);
    });
    it('lists the classes of a family with their overrides', () => {
        const flux = familyChips(classes, ['a', 'b'], false).find(c => c.family === 'flux')!;
        expect(flux.classes[0]).toMatchObject({ class: 'flux.stale', enabled: true, overridden: ['a'] });
        expect(overriddenIn(classes[4], ['b'])).toEqual([]);
    });
});

describe('totals and keys', () => {
    it('sums shown and acknowledged over enabled classes of active clusters', () => {
        expect(problemTotals(classes, ['a', 'b'])).toEqual({ shown: 3 + 3 + 1 + 1 + 4 - 4, acked: 3 });
    });
    it('lists classes with problems for group-by-class', () => {
        expect(classesWithProblems(classes, ['b'], [])).toEqual(['pod.crashloop', 'node.not-ready', 'flux.stale'].filter(c => c !== 'flux.stale'));
        expect(classesWithProblems(classes, ['a'], ['pods'])).toEqual(['pod.crashloop', 'pod.pending']);
    });
    it('sorts group keys by code unit, empty first', () => {
        expect(sortGroupKeys(['b', 'B', '', 'a', 'a'])).toEqual(['', 'B', 'a', 'b']);
    });
});

describe('query', () => {
    const f = { families: [] as string[], severity: 'all' as const, showAcked: false, text: '', namespaces: [] as string[] };
    const plain = { group: 'none' as const, oldest: false, aggregate: false };
    it('defaults: unacked only, no sort', () => {
        const q = buildProblemsQuery(f, plain);
        expect(q.filters).toEqual([{ col: 'acked', op: 'eq', value: 'false' }]);
        expect(q.sort).toEqual([]);
        expect(q.gvr).toBe('v8s/v1/problems');
    });
    it('builds family, severity, text, namespaces and acknowledged filters', () => {
        const q = buildProblemsQuery({ ...f, families: ['pods'], severity: 'error', showAcked: true, text: ' api ', namespaces: ['x'] }, plain);
        expect(q.filters).toEqual([
            { col: 'family', op: 'in', value: ['pods'] },
            { col: 'severity', op: 'in', value: ['critical', 'error'] }
        ]);
        expect(q.text).toBe('api');
        expect(q.namespaces).toEqual(['x']);
        expect(severityValues('critical')).toEqual(['critical']);
    });
    it('sorts by group column first, then severity and recency; oldest uses since', () => {
        expect(buildProblemsQuery(f, { ...plain, group: 'cluster' }).sort.map(s => s.col + (s.desc ? '-' : ''))).toEqual([
            'cluster',
            'severityRank',
            'lastChanged-',
            'name'
        ]);
        expect(buildProblemsQuery(f, { ...plain, oldest: true }).sort.map(s => s.col)).toEqual(['severityRank', 'since', 'name']);
    });
    it('aggregation sorts by identity', () => {
        expect(buildProblemsQuery(f, { ...plain, aggregate: true }).sort.map(s => s.col)).toEqual(['class', 'kind', 'namespace', 'name', 'cluster']);
    });
    it('builds group count queries', () => {
        expect(groupCountQuery(f, 'cluster', 'c1').clusters).toEqual(['c1']);
        expect(groupCountQuery({ ...f, namespaces: ['a', 'b'] }, 'namespace', 'a').namespaces).toEqual(['a']);
        expect(groupCountQuery(f, 'class', 'pod.crashloop').filters).toContainEqual({ col: 'class', op: 'eq', value: 'pod.crashloop' });
        expect(groupCountQuery(f, 'class', 'x').sort).toEqual([]);
    });
});

describe('rows', () => {
    const cols: Column[] = [
        'cluster',
        'name',
        'namespace',
        'severity',
        'severityRank',
        'class',
        'family',
        'kind',
        'title',
        'message',
        'since',
        'lastChanged',
        'count',
        'source',
        'acked',
        'statusTone'
    ].map(key => ({ key, type: 'str' as never }));
    it('indexes by column key and tolerates a missing problemId column', () => {
        const pc = problemCols(cols);
        expect(pc.cluster).toBe(2);
        expect(pc.acked).toBe(16);
        expect(pc.problemId).toBe(-1);
        const row = [1, 1, 'c1', 'api', 'ns', 'error', 1, 'pod.crashloop', 'pods', 'Deployment', 'T', 'M', 100, 200, 3, '', 'true', 0] as never;
        expect(readRow(pc, row)).toMatchObject({ cluster: 'c1', name: 'api', kind: 'Deployment', count: 3, acked: true, problemId: '', since: 100 });
        expect(problemCols(null).cluster).toBe(-1);
    });
    it('reads the problemId column when present', () => {
        const pc = problemCols([...cols, { key: 'problemId', type: 'str' as never }]);
        expect(pc.problemId).toBe(18);
    });
    it('recognises rollups and aggregate identity', () => {
        expect(isRollup({ class: 'pod.crashloop', count: 3, kind: 'Deployment' })).toBe(true);
        expect(isRollup({ class: 'pod.crashloop', count: 1, kind: 'Pod' })).toBe(false);
        expect(isRollup({ class: 'vuln.critical', count: 9, kind: 'Image' })).toBe(false);
        expect(aggregateKey({ class: 'a', kind: 'k', namespace: 'n', name: 'x' })).not.toBe(
            aggregateKey({ class: 'a', kind: 'k', namespace: 'n', name: 'y' })
        );
    });
});

describe('acknowledge', () => {
    it('computes snooze deadlines', () => {
        expect(snoozeUntil(1000, '1h')).toBe(1000 + 3600e3);
        expect(snoozeUntil(0, '1d')).toBe(86400e3);
        expect(snoozeUntil(5, '1w')).toBe(5 + 7 * 86400e3);
    });
});

describe('presets', () => {
    it('reads valid presets only', () => {
        const p = presetsOf({
            problemPrefs: {
                presets: { prod: { classes: { 'pod.pending': true, bad: 3 } }, junk: 4, arr: { classes: [] } },
                productionGroups: ['prod']
            }
        });
        expect(p).toEqual({ prod: { classes: { 'pod.pending': true } } });
        expect(presetsOf({ problemPrefs: {} })).toEqual({});
    });
    it('saves the current class set and applies it in one patch', () => {
        const set = currentClassSet(classes);
        expect(set['network.policy-drops']).toBe(false);
        expect(savePresetPatch('staging', set).problemPrefs).toEqual({ presets: { staging: { classes: set } } });
        const patch = applyPresetPatch({ classes: { 'pod.pending': false, 'unknown.class': true } }, classes);
        expect(patch).toEqual({ problemPrefs: { classes: { 'pod.pending': false } } });
        expect(presetDiff({ classes: { 'pod.pending': false, 'pod.crashloop': true } }, classes)).toBe(1);
    });
    it('class patches never overwrite productionGroups or presets (merge semantics)', () => {
        const doc = { problemPrefs: { productionGroups: ['prod'], presets: { a: { classes: { x: true } } }, classes: { 'pod.pending': true } } };
        const merged = mergePatch(doc, classPatch({ 'pod.pending': false, 'flux.stale': true }));
        expect(merged.problemPrefs).toEqual({
            productionGroups: ['prod'],
            presets: { a: { classes: { x: true } } },
            classes: { 'pod.pending': false, 'flux.stale': true }
        });
        const saved = mergePatch(doc, savePresetPatch('b', { y: false }));
        expect(saved.problemPrefs.productionGroups).toEqual(['prod']);
        expect(saved.problemPrefs.classes).toEqual({ 'pod.pending': true });
    });
});

describe('actionsForClass', () => {
    it('maps classes to fitting actions', () => {
        expect(actionsForClass('pod.crashloop', 'Pod')).toEqual(['logs', 'previous', 'describe']);
        expect(actionsForClass('pod.crashloop', 'Deployment')).toContain('restart');
        expect(actionsForClass('workload.rollout-stuck', 'Deployment')).toEqual(['logs', 'describe', 'restart', 'rollback']);
        expect(actionsForClass('helm.failed', 'Release')).toEqual(['release']);
        expect(actionsForClass('flux.not-ready', 'Kustomization')).toEqual(['describe', 'reconcile', 'suspend']);
        expect(actionsForClass('flux.suspended', 'Kustomization')).toEqual(['describe', 'reconcile']);
        expect(actionsForClass('vuln.critical', 'Image')).toEqual(['vulns']);
        expect(actionsForClass('policy.fail', 'Pod')).toEqual(['policy', 'describe']);
        expect(actionsForClass('node.not-ready', 'Node')).toEqual(['describe']);
    });
});

describe('refresher', () => {
    beforeEach(() => vi.useFakeTimers());
    afterEach(() => vi.useRealTimers());
    it('coalesces bursts into one call after the delay', () => {
        const run = vi.fn();
        const r = makeRefresher(run);
        r.schedule();
        r.schedule();
        vi.advanceTimersByTime(900);
        r.schedule();
        expect(run).not.toHaveBeenCalled();
        vi.advanceTimersByTime(100);
        expect(run).toHaveBeenCalledTimes(1);
    });
    it('never runs more than once per second under steady triggers', () => {
        const run = vi.fn();
        const r = makeRefresher(run);
        for (let t = 0; t < 10000; t += 100) {
            r.schedule();
            vi.advanceTimersByTime(100);
        }
        expect(run.mock.calls.length).toBeLessThanOrEqual(10);
        expect(run.mock.calls.length).toBeGreaterThanOrEqual(8);
    });
    it('can be cancelled', () => {
        const run = vi.fn();
        const r = makeRefresher(run);
        r.schedule();
        r.cancel();
        vi.advanceTimersByTime(5000);
        expect(run).not.toHaveBeenCalled();
    });
});
