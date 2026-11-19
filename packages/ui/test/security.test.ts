import type { ImageVulnSummary, ReportDetail } from '@v8s/client';

import { ExposureIngress, ExposureLoadBalancer, ExposureNodePort, ExposureService, ExposureUnknown } from '@v8s/client';
import { describe, expect, it } from 'vitest';

import {
    ChunkAccumulator,
    columnsFor,
    containerImages,
    DEFAULT_EXPOSURE_DEF,
    exposureList,
    filterImages,
    imageKey,
    matchesWorkload,
    matchImages,
    normExposureDef,
    normExposureFilter,
    NOT_EXPOSED,
    rowsOf,
    safeHttpUrl,
    sevRank,
    sevTone,
    shownExposure,
    sortImages,
    splitImage,
    viewRows,
    wantsMore,
    workloadSearchTerm,
    workloadTargetOf
} from '../src/lib/security.ts';

const img = (image: string, p: Partial<ImageVulnSummary> = {}): ImageVulnSummary => {
    const { repository, tag } = splitImage(image);
    return {
        image,
        repository,
        tag,
        source: 'trivy',
        critical: 0,
        high: 0,
        medium: 0,
        low: 0,
        unknown: 0,
        workloads: 1,
        reports: 1,
        clusters: ['a'],
        ...p
    };
};

describe('images', () => {
    const list = [
        img('b/low:1', { low: 5 }),
        img('a/crit:1', { critical: 1, high: 2, workloads: 3 }),
        img('c/crit:2', { critical: 4, source: 'kubescape', clusters: ['a', 'b'] }),
        img('d/high:1', { high: 9, workloads: 40 })
    ];

    it('ranks by critical then high by default', () => {
        expect(sortImages(list).map(i => i.image)).toEqual(['c/crit:2', 'a/crit:1', 'd/high:1', 'b/low:1']);
    });
    it('sorts by a column in either direction with a stable tie-break', () => {
        expect(sortImages(list, 'workloads', true)[0].image).toBe('d/high:1');
        expect(sortImages(list, 'image', false).map(i => i.image)).toEqual(['a/crit:1', 'b/low:1', 'c/crit:2', 'd/high:1']);
        expect(sortImages(list, 'clusters', true)[0].image).toBe('c/crit:2');
        expect(sortImages(list, 'source', false)[0].source).toBe('kubescape');
    });
    it('does not mutate the response', () => {
        const copy = [...list];
        sortImages(list, 'image', true);
        expect(list).toEqual(copy);
    });
    it('filters by text, source and only-critical', () => {
        expect(filterImages(list, { text: 'CRIT', source: '', onlyCritical: false })).toHaveLength(2);
        expect(filterImages(list, { text: '', source: 'kubescape', onlyCritical: false }).map(i => i.image)).toEqual(['c/crit:2']);
        expect(filterImages(list, { text: '', source: '', onlyCritical: true })).toHaveLength(2);
        expect(filterImages(list, { text: 'high', source: 'trivy', onlyCritical: true })).toHaveLength(0);
    });
});

describe('exposure', () => {
    const SVC = ExposureService;
    const LB = ExposureLoadBalancer | SVC;
    const ING = ExposureIngress | SVC;
    const NP = ExposureNodePort | SVC;
    const WITH_SVC = DEFAULT_EXPOSURE_DEF | SVC;
    const list = [
        img('lb:1', { exposure: LB }),
        img('ing:1', { exposure: ING | LB }),
        img('svc:1', { exposure: SVC }),
        img('none:1'),
        img('np:1', { exposure: NP })
    ];
    const names = (l: ImageVulnSummary[]) => l.map(i => i.image);

    it('shows only the kinds of the definition, and Service only when nothing wider applies', () => {
        expect(shownExposure(SVC, DEFAULT_EXPOSURE_DEF)).toBe(0);
        expect(shownExposure(SVC, WITH_SVC)).toBe(SVC);
        expect(shownExposure(LB, WITH_SVC)).toBe(ExposureLoadBalancer);
        expect(shownExposure(ING | LB, ExposureLoadBalancer)).toBe(ExposureLoadBalancer);
        expect(shownExposure(undefined, WITH_SVC)).toBe(0);
        expect(exposureList(ExposureIngress | ExposureNodePort).map(e => e.label)).toEqual(['Ingress', 'NodePort']);
    });
    it('filters by any checked kind, or not exposed, under the definition', () => {
        const f = (exposedVia: number, exposureDef = DEFAULT_EXPOSURE_DEF) =>
            names(filterImages(list, { text: '', source: '', onlyCritical: false, exposedVia, exposureDef }));
        expect(f(0)).toHaveLength(5);
        expect(f(ExposureLoadBalancer)).toEqual(['lb:1', 'ing:1']);
        expect(f(ExposureIngress | ExposureNodePort)).toEqual(['ing:1', 'np:1']);
        expect(f(NOT_EXPOSED)).toEqual(['svc:1', 'none:1']);
        expect(f(NOT_EXPOSED, WITH_SVC)).toEqual(['none:1']);
        expect(f(SVC, WITH_SVC)).toEqual(['svc:1']);
    });
    it('sorts by the widest shown kind', () => {
        expect(names(sortImages(list, 'exposure', true)).slice(0, 3)).toEqual(['ing:1', 'lb:1', 'np:1']);
        expect(names(sortImages(list, 'exposure', true, WITH_SVC)).slice(3)).toEqual(['svc:1', 'none:1']);
    });
    it('keeps unknown rows out of "not exposed" and finds them with Unknown', () => {
        const withUnknown = [...list, img('unk:1', { exposure: ExposureUnknown }), img('part:1', { exposure: ExposureUnknown | NP })];
        const f = (exposedVia: number) => names(filterImages(withUnknown, { text: '', source: '', onlyCritical: false, exposedVia }));
        expect(f(NOT_EXPOSED)).toEqual(['svc:1', 'none:1']);
        expect(f(ExposureUnknown)).toEqual(['unk:1', 'part:1']);
        expect(f(ExposureNodePort)).toEqual(['np:1', 'part:1']);
        expect(names(sortImages(withUnknown, 'exposure', true)).slice(-3)).toEqual(['unk:1', 'none:1', 'svc:1']);
        expect(normExposureFilter(ExposureUnknown | SVC, DEFAULT_EXPOSURE_DEF)).toBe(ExposureUnknown);
    });
    it('normalizes the definition and drops filter kinds it no longer includes', () => {
        expect(normExposureDef(0)).toBe(DEFAULT_EXPOSURE_DEF);
        expect(normExposureDef(SVC | (1 << 12))).toBe(SVC);
        expect(normExposureFilter(SVC | ExposureIngress | NOT_EXPOSED, DEFAULT_EXPOSURE_DEF)).toBe(ExposureIngress | NOT_EXPOSED);
    });
});

describe('severity', () => {
    it('orders and tones', () => {
        expect(['LOW', 'CRITICAL', 'weird', 'High', 'UNKNOWN'].sort((a, b) => sevRank(a) - sevRank(b))).toEqual([
            'CRITICAL',
            'High',
            'LOW',
            'UNKNOWN',
            'weird'
        ]);
        expect(sevTone('CRITICAL')).toBe('danger');
        expect(sevTone('high')).toBe('warning');
        expect(sevTone('Medium')).toBe('info');
        expect(sevTone('low')).toBe('neutral');
    });
});

describe('image names', () => {
    it('splits repository and tag, honouring registry ports and digests', () => {
        expect(splitImage('nginx:1.25')).toEqual({ repository: 'nginx', tag: '1.25' });
        expect(splitImage('reg:5000/a/b')).toEqual({ repository: 'reg:5000/a/b', tag: '' });
        expect(splitImage('reg:5000/a/b:2@sha256:abc')).toEqual({ repository: 'reg:5000/a/b', tag: '2' });
    });
    it('normalises to the scanner repository form', () => {
        expect(imageKey('nginx')).toBe('library/nginx:latest');
        expect(imageKey('docker.io/library/nginx:1.25.3')).toBe('library/nginx:1.25.3');
        expect(imageKey('ghcr.io/acme/api:1.2')).toBe('acme/api:1.2');
        expect(imageKey('bitnami/kafka:3.5.1')).toBe('bitnami/kafka:3.5.1');
        expect(imageKey('localhost:5000/x:1')).toBe('library/x:1');
    });
});

describe('container images', () => {
    const c = (name: string, image: string) => ({ name, image });
    it('reads a pod', () => {
        const parsed = { spec: { containers: [c('a', 'x:1'), c('b', 'y:2')], initContainers: [c('i', 'z:3')] } };
        expect(containerImages(parsed)).toEqual([
            { container: 'a', image: 'x:1', init: false },
            { container: 'b', image: 'y:2', init: false },
            { container: 'i', image: 'z:3', init: true }
        ]);
    });
    it('reads workloads and cronjobs, ignores junk', () => {
        expect(containerImages({ spec: { template: { spec: { containers: [c('a', 'x:1'), c('a', 'dup:1'), { name: 'n' }] } } } })).toHaveLength(1);
        expect(containerImages({ spec: { jobTemplate: { spec: { template: { spec: { containers: [c('j', 'q:1')] } } } } } })[0].image).toBe('q:1');
        expect(containerImages(null)).toEqual([]);
        expect(containerImages({ spec: {} })).toEqual([]);
    });
    it('matches against the overview regardless of registry', () => {
        const found = matchImages(
            [
                { container: 'a', image: 'nginx:1.25.3', init: false },
                { container: 'b', image: 'ghcr.io/none/here:1', init: false }
            ],
            [img('library/nginx:1.25.3'), img('library/nginx:1.25.3', { source: 'kubescape' })]
        );
        expect(found[0].found.map(f => f.source)).toEqual(['trivy', 'kubescape']);
        expect(found[1].found).toEqual([]);
    });
});

describe('workload matching', () => {
    const t = (gvr: string, name: string, owners: { kind: string; name: string }[] = []) =>
        workloadTargetOf(gvr, name, 'ns', owners.length ? { metadata: { ownerReferences: owners } } : null);
    it('matches a deployment to its ReplicaSets only', () => {
        const d = t('apps/v1/deployments', 'svc-1');
        expect(matchesWorkload(d, 'svc-1')).toBe(true);
        expect(matchesWorkload(d, 'svc-1-7d9f8b6c4')).toBe(true);
        expect(matchesWorkload(d, 'svc-1-api-7d9f8b6c4')).toBe(false);
        expect(matchesWorkload(d, 'svc-10-7d9f8b6c4')).toBe(false);
        expect(matchesWorkload(d, '')).toBe(false);
    });
    it('matches a pod through its owner (and the owner deployment)', () => {
        const p = t('v1/pods', 'svc-1-7d9f8b6c4-abcde', [{ kind: 'ReplicaSet', name: 'svc-1-7d9f8b6c4' }]);
        expect(matchesWorkload(p, 'svc-1-7d9f8b6c4')).toBe(true);
        expect(matchesWorkload(p, 'svc-1')).toBe(true);
        expect(matchesWorkload(p, 'svc-2')).toBe(false);
        expect(workloadSearchTerm(p)).toBe('svc-1');
    });
    it('matches cronjob children by numeric suffix', () => {
        const c = t('batch/v1/cronjobs', 'nightly');
        expect(matchesWorkload(c, 'nightly-28391045')).toBe(true);
        expect(matchesWorkload(c, 'nightly-abc')).toBe(false);
    });
});

describe('report rows', () => {
    const d: ReportDetail = {
        kind: 'vulnerabilities',
        source: 'trivy',
        total: 3,
        offset: 0,
        vulns: [
            { id: 'CVE-1', severity: 'LOW', package: 'a', installed: '1', link: 'javascript:alert(1)' },
            { id: 'CVE-2', severity: 'CRITICAL', package: 'b', installed: '1', fixed: '2', title: 'Bad thing', link: 'https://avd.example/cve-2' },
            { id: 'CVE-3', severity: 'HIGH', package: 'a', installed: '1' }
        ]
    };
    it('maps findings, highlights fixes and only keeps http(s) links', () => {
        const rows = rowsOf(d);
        expect(rows.map(r => r.id)).toEqual(['CVE-1', 'CVE-2', 'CVE-3']);
        expect(rows[0].href).toBeUndefined();
        expect(rows[1].href).toBe('https://avd.example/cve-2');
        expect(rows[1].hi).toBe(true);
        expect(rows[0].hi).toBe(false);
        expect(safeHttpUrl('ftp://x')).toBeUndefined();
        expect(safeHttpUrl('not a url')).toBeUndefined();
    });
    it('sorts worst first by default and filters by severity and text', () => {
        const rows = rowsOf(d);
        const cols = columnsFor('vulnerabilities');
        expect(viewRows(rows, new Set(), '', null, cols).map(r => r.id)).toEqual(['CVE-2', 'CVE-3', 'CVE-1']);
        expect(viewRows(rows, new Set(['low', 'high']), '', null, cols).map(r => r.id)).toEqual(['CVE-3', 'CVE-1']);
        expect(viewRows(rows, new Set(), 'bad thing', null, cols).map(r => r.id)).toEqual(['CVE-2']);
        expect(viewRows(rows, new Set(), '', { col: 'package', desc: true }, cols).map(r => r.id)).toEqual(['CVE-2', 'CVE-1', 'CVE-3']);
        expect(viewRows(rows, new Set(), '', { col: 'id', desc: true }, cols)[0].id).toBe('CVE-3');
    });
    it('maps checks, secrets and policy results', () => {
        expect(
            rowsOf({
                kind: 'checks',
                source: 'trivy',
                total: 1,
                offset: 0,
                checks: [{ source: 'trivy', id: 'KSV1', title: 'T', severity: 'HIGH', messages: ['m1', 'm2'], frameworks: ['NSA'] }]
            })[0].cells
        ).toEqual(['T', '', 'm1 m2', 'NSA']);
        expect(
            rowsOf({ kind: 'secrets', source: 'trivy', total: 1, offset: 0, secrets: [{ ruleId: 'aws', severity: 'CRITICAL', title: 'AWS' }] })[0]
                .tone
        ).toBe('danger');
        const pr = rowsOf({
            kind: 'policyResults',
            source: 'kyverno',
            total: 2,
            offset: 0,
            results: [
                { policy: 'p', result: 'pass' },
                { policy: 'p', rule: 'r', result: 'fail', message: 'nope' }
            ]
        });
        expect(pr.map(r => r.tone)).toEqual(['success', 'danger']);
        expect(viewRows(pr, new Set(), '', null, columnsFor('policyResults'))[0].sev).toBe('fail'); // failures first
    });
});

describe('chunk accumulator', () => {
    it('advances offset by rows until offset + rows >= total', () => {
        const a = new ChunkAccumulator<number>();
        expect(a.nextOffset).toBe(0);
        expect(a.accept(0, [1, 2, 3], 7, 'rv1')).toBe('ok');
        expect(a.nextOffset).toBe(3);
        expect(a.done).toBe(false);
        expect(a.accept(3, [4, 5, 6], 7, 'rv1')).toBe('ok');
        expect(a.done).toBe(false);
        expect(a.accept(6, [7], 7, 'rv1')).toBe('ok');
        expect(a.done).toBe(true);
        expect(a.rows).toEqual([1, 2, 3, 4, 5, 6, 7]);
    });
    it('stops on an empty chunk and drops stale offsets', () => {
        const a = new ChunkAccumulator<number>();
        a.accept(0, [1, 2], 10, 'x');
        expect(a.accept(0, [9], 10, 'x')).toBe('stale');
        expect(a.rows).toEqual([1, 2]);
        a.accept(2, [], 10, 'x');
        expect(a.done).toBe(true);
    });
    it('restarts when the report changed mid-read', () => {
        const a = new ChunkAccumulator<number>();
        a.accept(0, [1, 2], 4, 'rv1');
        expect(a.accept(2, [3, 4], 4, 'rv2')).toBe('restart');
        expect(a.rows).toEqual([]);
        expect(a.nextOffset).toBe(0);
        expect(a.accept(0, [1, 2, 3, 4], 4, 'rv2')).toBe('ok');
        expect(a.done).toBe(true);
    });
    it('reads eagerly up to the limit, then on demand near the loaded end', () => {
        const a = { rows: Array.from({ length: 100 }, () => 0), done: false };
        expect(wantsMore(a, 0, 60, 3000)).toBe(true);
        const big = { rows: Array.from({ length: 3300 }, () => 0), done: false };
        expect(wantsMore(big, 10, 60, 3000)).toBe(false);
        expect(wantsMore(big, 3250, 60, 3000)).toBe(true);
        expect(wantsMore({ rows: [], done: true }, 0)).toBe(false);
    });
});
