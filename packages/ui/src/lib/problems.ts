import type { Column, Filter, ProblemClassInfo, ProblemsEvidenceResult, Row, Settings, Sort, ViewQuery } from '@v8s/client';

import { ProblemsGVR } from '@v8s/client';

// ---- severity ----

export type SeverityFilter = 'all' | 'error' | 'critical';

export const SEVERITY_FILTERS: { id: SeverityFilter; label: string }[] = [
    { id: 'all', label: 'All' },
    { id: 'error', label: 'Error and up' },
    { id: 'critical', label: 'Critical' }
];

/** `severity in` values for the segmented control; empty means no filter. */
export function severityValues(f: SeverityFilter): string[] {
    return f === 'critical' ? ['critical'] : f === 'error' ? ['critical', 'error'] : [];
}

export function severityTone(sev: string): 'danger' | 'warning' | 'info' {
    return sev === 'critical' || sev === 'error' ? 'danger' : sev === 'warning' ? 'warning' : 'info';
}

// ---- families ----

const FAMILY_ORDER = [
    'pods',
    'workloads',
    'jobs',
    'nodes',
    'network',
    'storage',
    'flux',
    'helm',
    'policy',
    'vulnerabilities',
    'flows',
    'platform',
    'certificates'
];
const FAMILY_LABEL: Record<string, string> = {
    pods: 'Pods',
    workloads: 'Workloads',
    jobs: 'Jobs',
    nodes: 'Nodes',
    network: 'Network',
    storage: 'Storage',
    flux: 'Flux',
    helm: 'Helm',
    policy: 'Policy',
    vulnerabilities: 'Vulnerabilities',
    flows: 'Network flows',
    platform: 'Platform',
    certificates: 'Certificates'
};

export function familyLabel(f: string): string {
    return FAMILY_LABEL[f] ?? f.charAt(0).toUpperCase() + f.slice(1);
}

/** Stable order: the preferred list first, unknown families after it alphabetically. */
export function compareFamilies(a: string, b: string): number {
    const ia = FAMILY_ORDER.indexOf(a);
    const ib = FAMILY_ORDER.indexOf(b);
    if (ia >= 0 || ib >= 0) return (ia < 0 ? 1e6 : ia) - (ib < 0 ? 1e6 : ib);
    return a < b ? -1 : a > b ? 1 : 0;
}

// ---- class enablement (settings.problemPrefs) ----

/** Effective setting of a class in one cluster: per-cluster override, else the global value (the agent resolves default). */
export function enabledIn(info: ProblemClassInfo, cluster: string): boolean {
    return info.enabledIn?.[cluster] ?? info.enabled;
}

/** Enabled in at least one of `clusters` (all clusters when empty). */
export function enabledAnywhere(info: ProblemClassInfo, clusters: readonly string[]): boolean {
    if (!clusters.length) return info.enabled || Object.values(info.enabledIn ?? {}).some(Boolean);
    return clusters.some(c => enabledIn(info, c));
}

/** Clusters whose per-cluster override differs from the global value. */
export function overriddenIn(info: ProblemClassInfo, clusters: readonly string[]): string[] {
    const out: string[] = [];
    for (const [c, v] of Object.entries(info.enabledIn ?? {})) if (v !== info.enabled && (!clusters.length || clusters.includes(c))) out.push(c);
    return out.sort();
}

/**
 * Settings patch toggling classes globally. `problemPrefs` also carries unrelated keys (`productionGroups`, `presets`): the patch
 * only names `classes`, and the agent and the store merge it (RFC 7386), so nothing else is overwritten.
 */
export function classPatch(changes: Record<string, boolean>): Record<string, unknown> {
    return { problemPrefs: { classes: { ...changes } } };
}

// ---- chips ----

export interface FamilyClass {
    class: string;
    title: string;
    severity: string;
    count: number;
    enabled: boolean;
    overridden: string[];
}

export interface FamilyChip {
    family: string;
    label: string;
    /** Problems shown (acknowledged ones excluded unless `showAcked`), over the active clusters. */
    count: number;
    /** Every class of the family is disabled in every active cluster: the dashed `off` chip. */
    off: boolean;
    classes: FamilyClass[];
}

/** Live chip counts: sum of counts[cluster].total minus acked (unless `showAcked`) over the active clusters. Pure. */
export function familyChips(classes: readonly ProblemClassInfo[], activeClusters: readonly string[], showAcked: boolean): FamilyChip[] {
    const byFamily = new Map<string, FamilyChip>();
    for (const info of classes) {
        let chip = byFamily.get(info.family);
        if (!chip) {
            chip = { family: info.family, label: familyLabel(info.family), count: 0, off: true, classes: [] };
            byFamily.set(info.family, chip);
        }
        let n = 0;
        for (const c of activeClusters) {
            const cnt = info.counts?.[c];
            if (cnt && enabledIn(info, c)) n += showAcked ? cnt.total : cnt.total - cnt.acked;
        }
        const enabled = enabledAnywhere(info, activeClusters);
        if (enabled) {
            chip.off = false;
            chip.count += n;
        }
        chip.classes.push({
            class: info.class,
            title: info.title,
            severity: info.severity,
            count: n,
            enabled: info.enabled,
            overridden: overriddenIn(info, activeClusters)
        });
    }
    return [...byFamily.values()].sort((a, b) => compareFamilies(a.family, b.family));
}

export interface ProblemTotals {
    shown: number;
    acked: number;
}

/** Header summary: problems shown (not acknowledged) and acknowledged ones hidden, enabled classes over the active clusters. */
export function problemTotals(classes: readonly ProblemClassInfo[], activeClusters: readonly string[]): ProblemTotals {
    let shown = 0;
    let acked = 0;
    for (const info of classes) {
        if (!enabledAnywhere(info, activeClusters)) continue;
        for (const c of activeClusters) {
            const cnt = info.counts?.[c];
            if (!cnt || !enabledIn(info, c)) continue;
            shown += cnt.total - cnt.acked;
            acked += cnt.acked;
        }
    }
    return { shown, acked };
}

/** Class ids with at least one problem (group-by-class keys), over the active clusters. */
export function classesWithProblems(classes: readonly ProblemClassInfo[], activeClusters: readonly string[], families: readonly string[]): string[] {
    const out: string[] = [];
    for (const info of classes) {
        if (families.length && !families.includes(info.family)) continue;
        if (activeClusters.some(c => (info.counts?.[c]?.total ?? 0) > 0)) out.push(info.class);
    }
    return out;
}

// ---- query ----

export type GroupBy = 'none' | 'class' | 'cluster' | 'namespace';

export interface ProblemsFilters {
    families: readonly string[];
    severity: SeverityFilter;
    showAcked: boolean;
    text: string;
    namespaces: readonly string[];
    clusters?: readonly string[];
}

export interface ProblemsSort {
    group: GroupBy;
    oldest: boolean;
    /** Cross-cluster aggregation: sorted by identity so equal problems are adjacent. */
    aggregate: boolean;
}

export function filterList(f: ProblemsFilters): Filter[] {
    const out: Filter[] = [];
    if (f.families.length) out.push({ col: 'family', op: 'in', value: [...f.families] });
    const sev = severityValues(f.severity);
    if (sev.length) out.push({ col: 'severity', op: 'in', value: sev });
    if (!f.showAcked) out.push({ col: 'acked', op: 'eq', value: 'false' });
    return out;
}

export function sortList(s: ProblemsSort): Sort[] {
    if (s.aggregate)
        return [
            { col: 'class', desc: false },
            { col: 'kind', desc: false },
            { col: 'namespace', desc: false },
            { col: 'name', desc: false },
            { col: 'cluster', desc: false }
        ];
    const tail: Sort[] = s.oldest
        ? [
              { col: 'severityRank', desc: false },
              { col: 'since', desc: false },
              { col: 'name', desc: false }
          ]
        : [
              { col: 'severityRank', desc: false },
              { col: 'lastChanged', desc: true },
              { col: 'name', desc: false }
          ];
    if (s.group !== 'none') return [{ col: s.group, desc: false }, ...tail];
    // The default order omits `sort` so the agent serves its own default.
    return s.oldest ? tail : [];
}

export function buildProblemsQuery(f: ProblemsFilters, s: ProblemsSort): ViewQuery {
    return {
        clusters: [...(f.clusters ?? [])],
        gvr: ProblemsGVR,
        namespaces: [...f.namespaces],
        text: f.text.trim(),
        filters: filterList(f),
        sort: sortList(s)
    };
}

/** One-row count query for a group: the same filters plus the group's own. Sort is irrelevant for a count. */
export function groupCountQuery(f: ProblemsFilters, group: Exclude<GroupBy, 'none'>, key: string): ViewQuery {
    const q = buildProblemsQuery(f, { group: 'none', oldest: false, aggregate: false });
    if (group === 'cluster') q.clusters = [key];
    else if (group === 'namespace') q.namespaces = [key];
    else q.filters = [...q.filters, { col: 'class', op: 'eq', value: key }];
    return q;
}

/** Keys of a group, in the server's code-unit string order (empty namespace first). */
export function sortGroupKeys(keys: Iterable<string>): string[] {
    return [...new Set(keys)].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
}

export const MAX_GROUP_KEYS = 100;
export const AGGREGATE_CAP = 2000;

// ---- rows (indexed by the column keys of the handle) ----

export interface ProblemCols {
    cluster: number;
    name: number;
    namespace: number;
    severity: number;
    severityRank: number;
    class: number;
    family: number;
    kind: number;
    title: number;
    message: number;
    since: number;
    lastChanged: number;
    count: number;
    source: number;
    acked: number;
    problemId: number;
}

const KEYS = [
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
    'problemId'
] as const;

/** Row cell index of each column key (cells start at index 2 after id and version); -1 when the agent does not send a column. */
export function problemCols(columns: readonly Column[] | null): ProblemCols {
    const out = {} as Record<string, number>;
    for (const k of KEYS) {
        const i = columns ? columns.findIndex(c => c.key === k) : -1;
        out[k] = i < 0 ? -1 : i + 2;
    }
    return out as unknown as ProblemCols;
}

export interface ProblemRow {
    cluster: string;
    name: string;
    namespace: string;
    severity: string;
    class: string;
    kind: string;
    title: string;
    message: string;
    since: number;
    lastChanged: number;
    count: number;
    source: string;
    acked: boolean;
    /** The stable problem id (needed by evidence and ack); empty when the agent does not expose it. */
    problemId: string;
}

const str = (row: Row, i: number): string => (i < 0 ? '' : ((row[i] as string | null) ?? ''));
const num = (row: Row, i: number): number => (i < 0 ? 0 : Number(row[i] ?? 0));

export function readRow(cols: ProblemCols, row: Row): ProblemRow {
    return {
        cluster: str(row, cols.cluster),
        name: str(row, cols.name),
        namespace: str(row, cols.namespace),
        severity: str(row, cols.severity),
        class: str(row, cols.class),
        kind: str(row, cols.kind),
        title: str(row, cols.title),
        message: str(row, cols.message),
        since: num(row, cols.since),
        lastChanged: num(row, cols.lastChanged),
        count: num(row, cols.count),
        source: str(row, cols.source),
        acked: str(row, cols.acked) === 'true',
        problemId: str(row, cols.problemId)
    };
}

/** A rollup: several objects behind one row (a workload with failing pods, or a vulnerable image). */
export function isRollup(r: Pick<ProblemRow, 'class' | 'count' | 'kind'>): boolean {
    return r.count > 1 && (r.class.startsWith('pod.') || /^(Deployment|StatefulSet|DaemonSet|ReplicaSet|Job|CronJob)$/.test(r.kind));
}

/** Identity used to collapse the same problem across clusters. */
export const aggregateKey = (r: Pick<ProblemRow, 'class' | 'kind' | 'namespace' | 'name'>): string =>
    `${r.class}\u0000${r.kind}\u0000${r.namespace}\u0000${r.name}`;

// ---- acknowledge ----

export type SnoozePreset = '1h' | '1d' | '1w';
export const SNOOZE_PRESETS: { id: SnoozePreset; label: string }[] = [
    { id: '1h', label: 'Snooze 1 hour' },
    { id: '1d', label: 'Snooze 1 day' },
    { id: '1w', label: 'Snooze 1 week' }
];
const SNOOZE_MS: Record<SnoozePreset, number> = { '1h': 3600e3, '1d': 86400e3, '1w': 7 * 86400e3 };

/** Unix ms at which a snooze ends. */
export function snoozeUntil(nowMs: number, preset: SnoozePreset): number {
    return nowMs + SNOOZE_MS[preset];
}

// ---- presets (settings.problemPrefs.presets[group] = { classes }) ----

export interface ClassPreset {
    classes: Record<string, boolean>;
}

/** Presets by cluster group name; malformed entries are dropped. */
export function presetsOf(s: Pick<Settings, 'problemPrefs'>): Record<string, ClassPreset> {
    const raw = s.problemPrefs?.presets;
    const out: Record<string, ClassPreset> = {};
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return out;
    for (const [g, p] of Object.entries(raw as Record<string, unknown>)) {
        const cls = p && typeof p === 'object' ? (p as { classes?: unknown }).classes : null;
        if (!cls || typeof cls !== 'object' || Array.isArray(cls)) continue;
        const classes: Record<string, boolean> = {};
        for (const [k, v] of Object.entries(cls as Record<string, unknown>)) if (typeof v === 'boolean') classes[k] = v;
        out[g] = { classes };
    }
    return out;
}

/** The global enable/disable set as shown now: one boolean per registry class. */
export function currentClassSet(classes: readonly ProblemClassInfo[]): Record<string, boolean> {
    const out: Record<string, boolean> = {};
    for (const c of classes) out[c.class] = c.enabled;
    return out;
}

export function savePresetPatch(group: string, set: Record<string, boolean>): Record<string, unknown> {
    return { problemPrefs: { presets: { [group]: { classes: { ...set } } } } };
}

export function deletePresetPatch(group: string): Record<string, unknown> {
    return { problemPrefs: { presets: { [group]: null } } };
}

/** Applying a preset writes the class switches in one patch; classes the preset does not mention keep their setting. */
export function applyPresetPatch(preset: ClassPreset, known: readonly ProblemClassInfo[]): Record<string, unknown> {
    const changes: Record<string, boolean> = {};
    const names = new Set(known.map(k => k.class));
    for (const [cls, v] of Object.entries(preset.classes)) if (names.has(cls)) changes[cls] = v;
    return classPatch(changes);
}

/** Classes whose switch differs from the preset (what applying it would change). */
export function presetDiff(preset: ClassPreset, known: readonly ProblemClassInfo[]): number {
    let n = 0;
    for (const k of known) if (k.class in preset.classes && preset.classes[k.class] !== k.enabled) n++;
    return n;
}

// ---- refresh scheduling ----

export interface Refresher {
    /** Coalesces triggers: the call runs `delay` ms after the first one, never more often than once per `minGap`. */
    schedule(): void;
    cancel(): void;
}

export function makeRefresher(run: () => void, opts: { delay?: number; minGap?: number; now?: () => number } = {}): Refresher {
    const delay = opts.delay ?? 1000;
    const minGap = opts.minGap ?? 1000;
    const now = opts.now ?? Date.now;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let last = -Infinity;
    return {
        schedule() {
            if (timer !== undefined) return;
            const wait = Math.max(delay, last + minGap - now());
            timer = setTimeout(() => {
                timer = undefined;
                last = now();
                run();
            }, wait);
        },
        cancel() {
            clearTimeout(timer);
            timer = undefined;
        }
    };
}

// ---- quick actions by class ----

export type ProblemAction = 'logs' | 'previous' | 'describe' | 'restart' | 'rollback' | 'reconcile' | 'suspend' | 'release' | 'policy' | 'vulns';

const WORKLOADS = /^(Deployment|StatefulSet|DaemonSet)$/;

/** Which quick actions fit a class (and its subject kind). Acknowledge is always offered separately. */
export function actionsForClass(cls: string, kind = ''): ProblemAction[] {
    const out: ProblemAction[] = [];
    if (cls.startsWith('pod.')) {
        out.push('logs');
        if (/crashloop|oomkilled|restarting/.test(cls)) out.push('previous');
        out.push('describe');
        if (WORKLOADS.test(kind)) out.push('restart');
    } else if (cls.startsWith('workload.')) {
        out.push('logs', 'describe');
        if (WORKLOADS.test(kind)) out.push('restart');
        if (kind === 'Deployment' && /rollout-stuck|unavailable/.test(cls)) out.push('rollback');
    } else if (cls.startsWith('job.') || cls.startsWith('cronjob.')) {
        out.push('describe');
        if (kind === 'Job') out.push('logs');
    } else if (cls.startsWith('flux.')) {
        out.push('describe');
        if (!/controller-down/.test(cls) && kind !== 'Deployment') out.push('reconcile');
        if (!/suspended/.test(cls) && kind !== 'Deployment') out.push('suspend');
    } else if (cls.startsWith('helm.')) {
        out.push('release');
    } else if (cls.startsWith('policy.')) {
        out.push('policy', 'describe');
    } else if (cls.startsWith('vuln.') || cls.startsWith('misconfig.')) {
        out.push('vulns');
    } else {
        out.push('describe');
    }
    return out;
}

export const ACTION_LABEL: Record<ProblemAction, string> = {
    logs: 'Logs',
    previous: 'Previous logs',
    describe: 'Describe',
    restart: 'Restart',
    rollback: 'Roll back',
    reconcile: 'Reconcile',
    suspend: 'Suspend',
    release: 'Open release',
    policy: 'Open policy',
    vulns: 'Open image vulns'
};

/** Subject kinds that are not cluster objects with a table of their own. */
export function nonObjectSubject(kind: string): 'image' | 'release' | null {
    return kind === 'Image' ? 'image' : kind === 'Release' || kind === 'HelmRelease' ? 'release' : null;
}

// ---- evidence state ----

export interface EvState {
    /** The stable problem id the state belongs to. */
    pid: string;
    status: 'loading' | 'ok' | 'error';
    data?: ProblemsEvidenceResult;
    error?: string;
}

export const NO_PROBLEM_ID = 'This agent does not expose problem ids to the UI, so evidence and acknowledgement are unavailable.';
