import type { ImageVulnSummary, ReportDetail } from '@v8s/client';

import { ExposureIngress, ExposureLoadBalancer, ExposureNodePort, ExposureService, ExposureUnknown } from '@v8s/client';

import type { Tone } from './table-decor.ts';

// Pure logic of the Security section (no agent access: see security-api.ts for the requests).

export const SEVERITIES = ['critical', 'high', 'medium', 'low', 'unknown'] as const;
export type Severity = (typeof SEVERITIES)[number];

const RANK: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3, negligible: 4, none: 4, unknown: 5 };

/** 0 = worst. Unrecognised words sort last. */
export function sevRank(s: string): number {
    return RANK[s.toLowerCase()] ?? 6;
}

/** Tone of a severity word or a count column key. */
export function sevTone(s: string): Tone {
    switch (s.toLowerCase()) {
        case 'critical':
            return 'danger';
        case 'high':
            return 'warning';
        case 'medium':
            return 'info';
        default:
            return 'neutral';
    }
}

export const sevLabel = (s: string): string => (s ? s[0].toUpperCase() + s.slice(1).toLowerCase() : '');

/** Tone of a Kyverno result word. */
export function resultTone(r: string): Tone {
    switch (r.toLowerCase()) {
        case 'fail':
        case 'error':
            return 'danger';
        case 'warn':
            return 'warning';
        case 'pass':
            return 'success';
        default:
            return 'neutral';
    }
}

const RESULT_RANK: Record<string, number> = { fail: 0, error: 1, warn: 2, skip: 3, pass: 4 };
export const resultRank = (r: string): number => RESULT_RANK[r.toLowerCase()] ?? 5;

export const sourceLabel = (s: string): string => (s === 'trivy' ? 'Trivy' : s === 'kubescape' ? 'Kubescape' : s ? sevLabel(s) : '');

// ---- exposure ("Exposed via") ----

/**
 * The exposure kinds of an image row (protocol `Exposure*` bits), widest first. The bit values double as the sort
 * rank (Ingress 8 > LoadBalancer 4 > NodePort 2 > Service 1).
 */
export const EXPOSURES = [
    { bit: ExposureIngress, label: 'Ingress', short: 'Ingress', hint: 'Ingress or Gateway API route' },
    { bit: ExposureLoadBalancer, label: 'LoadBalancer', short: 'LB', hint: 'Service of type LoadBalancer' },
    { bit: ExposureNodePort, label: 'NodePort', short: 'NodePort', hint: 'Service of type NodePort' },
    { bit: ExposureService, label: 'Service', short: 'Service', hint: 'Any Service (cluster-internal)' }
] as const;

/** What counts as exposed until the viewer changes it: everything reachable from outside the cluster (not a plain Service). */
export const DEFAULT_EXPOSURE_DEF = ExposureIngress | ExposureLoadBalancer | ExposureNodePort;
const ALL_EXPOSURE = DEFAULT_EXPOSURE_DEF | ExposureService;
/** Filter bit for rows with no exposure under the definition (outside the protocol bits). */
export const NOT_EXPOSED = 1 << 8;
/** Protocol bit (also a filter bit): a cluster of the row could not be resolved (its Services, EndpointSlices or Pods are still loading, or not readable). */
export const EXPOSURE_UNKNOWN = ExposureUnknown;
export const isExposureUnknown = (bits: number | undefined): boolean => ((bits ?? 0) & ExposureUnknown) !== 0;

/** Keeps only known definition bits; an empty definition falls back to the default. */
export const normExposureDef = (def: number): number => def & ALL_EXPOSURE || DEFAULT_EXPOSURE_DEF;

/**
 * The kinds a row shows under a definition: its bits within the definition. Service is dropped when a wider kind is shown
 * (every LoadBalancer / NodePort / routed workload is also behind a Service). 0 = not exposed (`-`).
 */
export function shownExposure(bits: number | undefined, def: number): number {
    const m = (bits ?? 0) & def;
    const wide = m & ~ExposureService;
    return wide || m;
}

/** Display entries of shown bits, widest first. */
export const exposureList = (shown: number) => EXPOSURES.filter(e => shown & e.bit);

/** Drops filter bits for kinds the definition no longer includes (NOT_EXPOSED and EXPOSURE_UNKNOWN stay). */
export const normExposureFilter = (filter: number, def: number): number => filter & (def | NOT_EXPOSED | ExposureUnknown);

/**
 * True when a row passes the "Exposed via" filter: no bits = no filtering; otherwise a shown kind is checked, or the row is
 * unknown and Unknown is checked, or it is known to be unexposed and Not exposed is checked (an unknown row is never "not exposed").
 */
export function matchesExposure(bits: number | undefined, def: number, filter: number): boolean {
    if (!filter) return true;
    const shown = shownExposure(bits, def);
    if (shown & filter) return true;
    if (isExposureUnknown(bits)) return (filter & ExposureUnknown) !== 0;
    return !shown && (filter & NOT_EXPOSED) !== 0;
}

// ---- images overview ----

export type SourceFilter = '' | 'trivy' | 'kubescape';
export interface ImageFilter {
    text: string;
    source: SourceFilter;
    onlyCritical: boolean;
    /** "Exposed via" filter bits (EXPOSURES bits, NOT_EXPOSED); 0 or absent = any. */
    exposedVia?: number;
    /** What counts as exposed (EXPOSURES bits); defaults to DEFAULT_EXPOSURE_DEF. */
    exposureDef?: number;
}

export function filterImages(images: readonly ImageVulnSummary[], f: ImageFilter): ImageVulnSummary[] {
    const t = f.text.trim().toLowerCase();
    const ev = f.exposedVia ?? 0;
    const def = f.exposureDef ?? DEFAULT_EXPOSURE_DEF;
    return images.filter(
        i =>
            (!t || i.image.toLowerCase().includes(t)) &&
            (!f.source || i.source === f.source) &&
            (!f.onlyCritical || i.critical > 0) &&
            (!ev || matchesExposure(i.exposure, def, ev))
    );
}

export type ImageSortKey =
    | 'image'
    | 'critical'
    | 'high'
    | 'medium'
    | 'low'
    | 'unknown'
    | 'workloads'
    | 'exposure'
    | 'reports'
    | 'clusters'
    | 'source';
export const DEFAULT_SORT: { key: ImageSortKey; desc: boolean } = { key: 'critical', desc: true };

const sevCmp = (a: ImageVulnSummary, b: ImageVulnSummary) =>
    b.critical - a.critical || b.high - a.high || b.medium - a.medium || b.low - a.low || b.unknown - a.unknown;

function valueOf(i: ImageVulnSummary, key: ImageSortKey, def: number): number | string {
    if (key === 'clusters') return i.clusters.length;
    // Unknown ranks between Service (1) and not exposed (0).
    if (key === 'exposure') return shownExposure(i.exposure, def) || (isExposureUnknown(i.exposure) ? 0.5 : 0);
    return i[key];
}

/**
 * Sorted copy. Ties fall back to the server's ranking (critical, high, ...) then the image name, so the order is total and stable.
 * `def` is the exposure definition the `exposure` key ranks by (widest shown kind first when descending).
 */
export function sortImages(
    images: readonly ImageVulnSummary[],
    key: ImageSortKey = DEFAULT_SORT.key,
    desc = DEFAULT_SORT.desc,
    def = DEFAULT_EXPOSURE_DEF
): ImageVulnSummary[] {
    const dir = desc ? -1 : 1;
    return [...images].sort((a, b) => {
        const va = valueOf(a, key, def);
        const vb = valueOf(b, key, def);
        const c = typeof va === 'number' && typeof vb === 'number' ? va - vb : String(va).localeCompare(String(vb));
        return c * dir || sevCmp(a, b) || a.image.localeCompare(b.image) || a.source.localeCompare(b.source);
    });
}

/** Totals of the filtered rows, for the page header. */
export function totalsOf(images: readonly ImageVulnSummary[]): Record<Severity, number> {
    const t = { critical: 0, high: 0, medium: 0, low: 0, unknown: 0 };
    for (const i of images) for (const s of SEVERITIES) t[s] += i[s];
    return t;
}

// ---- image names ----

/** `repository:tag` split at the tag colon (a colon before the last `/` belongs to a registry port). A digest is dropped. */
export function splitImage(image: string): { repository: string; tag: string } {
    const noDigest = image.split('@')[0];
    const slash = noDigest.lastIndexOf('/');
    const colon = noDigest.indexOf(':', slash + 1);
    return colon < 0 ? { repository: noDigest, tag: '' } : { repository: noDigest.slice(0, colon), tag: noDigest.slice(colon + 1) };
}

const isRegistry = (seg: string) => seg === 'localhost' || seg.includes('.') || seg.includes(':');

/**
 * Comparable key of an image reference: no registry host (Trivy stores `repository` without it), Docker Hub's implicit `library/`
 * and `latest`, no digest. `nginx` -> `library/nginx:latest`, `ghcr.io/acme/api:1.2` -> `acme/api:1.2`.
 */
export function imageKey(image: string): string {
    const { repository, tag } = splitImage(image.trim());
    const parts = repository.split('/');
    if (parts.length > 1 && isRegistry(parts[0])) parts.shift();
    if (parts.length === 1) parts.unshift('library');
    return `${parts.join('/')}:${tag || 'latest'}`;
}

export const keyOfSummary = (i: Pick<ImageVulnSummary, 'repository' | 'tag'>): string => imageKey(`${i.repository}:${i.tag}`);

// ---- containers of an object ----

export interface ContainerImage {
    container: string;
    image: string;
    init: boolean;
}

const rec = (v: unknown): Record<string, unknown> | undefined =>
    v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined;

/** The pod spec of a pod, a workload (`spec.template.spec`) or a CronJob (`spec.jobTemplate.spec.template.spec`). */
export function podSpecOf(parsed: Record<string, unknown> | null): Record<string, unknown> | undefined {
    const spec = rec(parsed?.spec);
    if (!spec) return undefined;
    if (Array.isArray(spec.containers)) return spec;
    return rec(rec(spec.template)?.spec) ?? rec(rec(rec(rec(spec.jobTemplate)?.spec)?.template)?.spec);
}

/** Containers (then init containers) of the object with their image references; unique per container name. */
export function containerImages(parsed: Record<string, unknown> | null): ContainerImage[] {
    const ps = podSpecOf(parsed);
    if (!ps) return [];
    const out: ContainerImage[] = [];
    const seen = new Set<string>();
    for (const [field, init] of [
        ['containers', false],
        ['initContainers', true]
    ] as const) {
        const list = ps[field];
        if (!Array.isArray(list)) continue;
        for (const c of list) {
            const m = rec(c);
            const image = typeof m?.image === 'string' ? m.image : '';
            const name = typeof m?.name === 'string' ? m.name : '';
            if (!image || seen.has(init + name)) continue;
            seen.add(init + name);
            out.push({ container: name, image, init });
        }
    }
    return out;
}

export interface ImageMatch extends ContainerImage {
    /** Summaries (one per scanner source) for this image among the response. */
    found: ImageVulnSummary[];
}

/** Matches an object's container images against the overview response. */
export function matchImages(containers: readonly ContainerImage[], images: readonly ImageVulnSummary[]): ImageMatch[] {
    const by = new Map<string, ImageVulnSummary[]>();
    for (const i of images) {
        const k = keyOfSummary(i);
        const l = by.get(k);
        if (l) l.push(i);
        else by.set(k, [i]);
    }
    return containers.map(c => ({ ...c, found: by.get(imageKey(c.image)) ?? [] }));
}

// ---- which scanner reports belong to a workload ----

export interface WorkloadTarget {
    kind: string;
    name: string;
    namespace: string;
    owners: { kind: string; name: string }[];
}

const KIND_OF_GVR: Record<string, string> = {
    'v1/pods': 'Pod',
    'apps/v1/deployments': 'Deployment',
    'apps/v1/statefulsets': 'StatefulSet',
    'apps/v1/daemonsets': 'DaemonSet',
    'apps/v1/replicasets': 'ReplicaSet',
    'batch/v1/jobs': 'Job',
    'batch/v1/cronjobs': 'CronJob'
};

export const kindOfGvr = (gvr: string): string => KIND_OF_GVR[gvr] ?? '';

/** Drops the pod-template-hash / suffix segment of a ReplicaSet or Job name. */
export const stripHash = (name: string): string => {
    const i = name.lastIndexOf('-');
    return i > 0 ? name.slice(0, i) : name;
};

export function workloadTargetOf(gvr: string, name: string, namespace: string | undefined, parsed: Record<string, unknown> | null): WorkloadTarget {
    const refs = rec(parsed?.metadata)?.ownerReferences;
    const owners: { kind: string; name: string }[] = [];
    if (Array.isArray(refs))
        for (const r of refs) {
            const m = rec(r);
            if (typeof m?.kind === 'string' && typeof m.name === 'string') owners.push({ kind: m.kind, name: m.name });
        }
    return { kind: kindOfGvr(gvr), name, namespace: namespace ?? '', owners };
}

/** Exact workload names whose reports describe this object. */
export function workloadNames(t: WorkloadTarget): string[] {
    const out = new Set<string>([t.name]);
    for (const o of t.owners) {
        out.add(o.name);
        if (o.kind === 'ReplicaSet' || o.kind === 'Job') out.add(stripHash(o.name)); // Kubescape reports the Deployment / CronJob itself
    }
    if (t.kind === 'ReplicaSet' || t.kind === 'Job') out.add(stripHash(t.name));
    return [...out];
}

/** The shortest candidate: the server-side `workload contains` filter, narrowed by `matchesWorkload`. */
export const workloadSearchTerm = (t: WorkloadTarget): string => workloadNames(t).reduce((a, b) => (b.length < a.length ? b : a));

/** True when a report row's `workload` cell belongs to the object (its own name, an owner's, or a Deployment's ReplicaSets / a CronJob's Jobs). */
export function matchesWorkload(t: WorkloadTarget, workload: string): boolean {
    if (!workload) return false;
    if (workloadNames(t).includes(workload)) return true;
    const rest = workload.startsWith(t.name + '-') ? workload.slice(t.name.length + 1) : '';
    if (!rest) return false;
    if (t.kind === 'Deployment') return /^[a-z0-9]{4,12}$/.test(rest);
    if (t.kind === 'CronJob') return /^\d+$/.test(rest);
    return false;
}

// ---- report detail rows ----

export type ReportKind = 'vulnerabilities' | 'checks' | 'secrets' | 'policyResults' | 'document';

export interface ReportCol {
    key: string;
    label: string;
    /** Grid track: px, or `fr` for the growing column. */
    width: string;
    mono?: boolean;
}

export interface ReportRow {
    /** Primary text (CVE id, check id, rule id, policy). */
    id: string;
    /** Severity word (or result word for policy results) shown as a tag. */
    sev: string;
    tone: Tone;
    rank: number;
    /** Remaining columns in `columnsFor(kind)` order after id and severity. */
    cells: string[];
    /** Present when `cells` holds a fix / remediation worth highlighting (a CVE with a fixed version). */
    hi: boolean;
    /** Safe http(s) link of the id, if any. */
    href?: string;
    /** Lower-cased haystack for the text filter. */
    hay: string;
}

export const REPORT_KIND_LABEL: Record<string, string> = {
    vulnerabilities: 'Vulnerabilities',
    checks: 'Checks',
    secrets: 'Secrets',
    policyResults: 'Policy results',
    document: 'Document'
};

export function columnsFor(kind: string): ReportCol[] {
    switch (kind) {
        case 'vulnerabilities':
            return [
                { key: 'id', label: 'CVE', width: '150px', mono: true },
                { key: 'sev', label: 'Severity', width: '88px' },
                { key: 'package', label: 'Package', width: '170px', mono: true },
                { key: 'installed', label: 'Installed', width: '110px', mono: true },
                { key: 'fixed', label: 'Fixed', width: '110px', mono: true },
                { key: 'title', label: 'Title', width: 'minmax(220px, 1fr)' }
            ];
        case 'checks':
            return [
                { key: 'id', label: 'Check', width: '110px', mono: true },
                { key: 'sev', label: 'Severity', width: '88px' },
                { key: 'title', label: 'Title', width: 'minmax(200px, 1.2fr)' },
                { key: 'category', label: 'Category', width: '150px' },
                { key: 'messages', label: 'Messages', width: 'minmax(200px, 1.5fr)' },
                { key: 'frameworks', label: 'Frameworks', width: '140px' }
            ];
        case 'secrets':
            return [
                { key: 'id', label: 'Rule', width: '170px', mono: true },
                { key: 'sev', label: 'Severity', width: '88px' },
                { key: 'title', label: 'Title', width: 'minmax(200px, 1fr)' },
                { key: 'category', label: 'Category', width: '120px' },
                { key: 'target', label: 'Target', width: '220px', mono: true },
                { key: 'match', label: 'Match', width: '200px', mono: true }
            ];
        default:
            return [
                { key: 'id', label: 'Policy', width: '200px' },
                { key: 'sev', label: 'Result', width: '88px' },
                { key: 'rule', label: 'Rule', width: '170px', mono: true },
                { key: 'severity', label: 'Severity', width: '90px' },
                { key: 'message', label: 'Message', width: 'minmax(240px, 1fr)' },
                { key: 'category', label: 'Category', width: '120px' }
            ];
    }
}

/** http(s) URLs only: a report is cluster content, so `javascript:` and friends never become links. */
export function safeHttpUrl(s: string | undefined): string | undefined {
    if (!s) return undefined;
    try {
        const u = new URL(s);
        return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : undefined;
    } catch {
        return undefined;
    }
}

function mk(id: string, sev: string, tone: Tone, rank: number, cells: string[], hi: boolean, href?: string): ReportRow {
    return { id, sev, tone, rank, cells, hi, href, hay: (id + ' ' + sev + ' ' + cells.join(' ')).toLowerCase() };
}

/** The rows of one ReportDetail chunk in display shape (kind-specific arrays are mapped, nothing else is kept). */
export function rowsOf(d: ReportDetail): ReportRow[] {
    switch (d.kind) {
        case 'vulnerabilities':
            return (d.vulns ?? []).map(v =>
                mk(
                    v.id,
                    v.severity,
                    sevTone(v.severity),
                    sevRank(v.severity),
                    [v.package, v.installed, v.fixed ?? '', v.title ?? ''],
                    !!v.fixed,
                    safeHttpUrl(v.link)
                )
            );
        case 'checks':
            return (d.checks ?? []).map(c =>
                mk(
                    c.id,
                    c.severity,
                    sevTone(c.severity),
                    sevRank(c.severity),
                    [c.title, c.category ?? '', (c.messages ?? []).join(' '), (c.frameworks ?? []).join(', ')],
                    false
                )
            );
        case 'secrets':
            return (d.secrets ?? []).map(s =>
                mk(s.ruleId, s.severity, sevTone(s.severity), sevRank(s.severity), [s.title, s.category ?? '', s.target ?? '', s.match ?? ''], false)
            );
        case 'policyResults':
            return (d.results ?? []).map(r =>
                mk(
                    r.policy,
                    r.result,
                    resultTone(r.result),
                    resultRank(r.result),
                    [r.rule ?? '', r.severity ?? '', r.message ?? '', r.category ?? ''],
                    false
                )
            );
        default:
            return [];
    }
}

/** Filters (severity words, text over every column) and sorts (worst first, then load order) loaded rows. */
export function viewRows(
    rows: readonly ReportRow[],
    sevs: ReadonlySet<string>,
    text: string,
    sort: { col: string; desc: boolean } | null,
    cols: readonly ReportCol[]
): ReportRow[] {
    const t = text.trim().toLowerCase();
    const idx = rows.map((r, i) => i);
    const keep = idx.filter(i => {
        const r = rows[i];
        return (!sevs.size || sevs.has(r.sev.toLowerCase())) && (!t || r.hay.includes(t));
    });
    const col = sort ? cols.findIndex(c => c.key === sort.col) : -1;
    const dir = sort?.desc ? -1 : 1;
    keep.sort((a, b) => {
        const ra = rows[a];
        const rb = rows[b];
        if (!sort || sort.col === 'sev') return (ra.rank - rb.rank) * (sort?.desc ? -1 : 1) || a - b;
        const va = col === 0 ? ra.id : (ra.cells[col - 2] ?? '');
        const vb = col === 0 ? rb.id : (rb.cells[col - 2] ?? '');
        return va.localeCompare(vb, undefined, { numeric: true }) * dir || a - b;
    });
    return keep.map(i => rows[i]);
}

// ---- chunked loading ----

/**
 * Accumulates `security.reportDetail` chunks: the next request is `offset = rows loaded`, until `offset + rows >= total`. A chunk
 * whose `resourceVersion` differs from the first (the report was replaced mid-read) restarts the read; a chunk for any other offset
 * is dropped as stale.
 */
export class ChunkAccumulator<T> {
    rows: T[] = [];
    total = 0;
    resourceVersion = '';
    done = false;

    get nextOffset(): number {
        return this.rows.length;
    }

    reset(): void {
        this.rows = [];
        this.total = 0;
        this.resourceVersion = '';
        this.done = false;
    }

    accept(offset: number, got: readonly T[], total: number, resourceVersion = ''): 'ok' | 'restart' | 'stale' {
        if (this.rows.length && resourceVersion && this.resourceVersion && resourceVersion !== this.resourceVersion) {
            this.reset();
            return 'restart';
        }
        if (offset !== this.rows.length) return 'stale';
        if (resourceVersion) this.resourceVersion = resourceVersion;
        for (const r of got) this.rows.push(r);
        this.total = total;
        this.done = got.length === 0 || this.rows.length >= total;
        return 'ok';
    }
}

/** Rows loaded eagerly (chunk after chunk) before reading continues on scroll. */
export const EAGER_ROWS = 3000;
export const CHUNK_LIMIT = 300;

/** Whether the next chunk should be requested now: eagerly below `EAGER_ROWS`, then when the viewport is within `margin` rows of the loaded end. */
export function wantsMore(acc: Pick<ChunkAccumulator<unknown>, 'rows' | 'done'>, lastVisible: number, margin = 60, eager = EAGER_ROWS): boolean {
    if (acc.done) return false;
    return acc.rows.length < eager || lastVisible >= acc.rows.length - margin;
}
