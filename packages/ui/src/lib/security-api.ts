import type {
    Column,
    ImageVulnSummary,
    Kind,
    KubescapeScanNowParams,
    ObjectPayload,
    ObjectRef,
    ReportDetail,
    Row,
    SecurityImagesResult,
    ViewQuery
} from '@v8s/client';

import { watch } from 'vue';

import { useClustersStore } from '@/stores/clusters.ts';
import { useUiStore } from '@/stores/ui.ts';

import { getAgent } from './agent.ts';
import { ask } from './ask.ts';
import { callMutating, errorText, isCancelled } from './mutations.ts';
import { sourceLabel } from './security.ts';

// Requests of the Security section and a short-lived cache of the image overview.

export async function fetchImagesRaw(clusters?: string[]): Promise<ImageVulnSummary[]> {
    const res = await getAgent().call<SecurityImagesResult>('security.images', clusters?.length ? { clusters } : {});
    return res.images ?? [];
}

const TTL_MS = 30_000;
const cache = new Map<string, { at: number; images: ImageVulnSummary[] }>();
const pending = new Map<string, Promise<ImageVulnSummary[]>>();

/**
 * `security.images` with a module-level cache (30 s) shared by every caller, so opening many drawers does not refetch. The cache is
 * keyed by the cluster scope; concurrent callers share one request. `force` bypasses the cache (the Refresh button).
 */
export function fetchImages(clusters: string[] = [], force = false): Promise<ImageVulnSummary[]> {
    const key = [...clusters].sort().join('\n');
    const hit = cache.get(key);
    if (!force && hit && Date.now() - hit.at < TTL_MS) return Promise.resolve(hit.images);
    let p = pending.get(key);
    if (!p) {
        p = fetchImagesRaw(clusters)
            .then(images => {
                cache.set(key, { at: Date.now(), images });
                return images;
            })
            .finally(() => pending.delete(key));
        pending.set(key, p);
    }
    return p;
}

export const clearImageCache = (): void => cache.clear();

export const reportDetail = (ref: ObjectRef, offset: number, limit: number): Promise<ReportDetail> =>
    getAgent().call<ReportDetail>('security.reportDetail', { ref, offset, limit });

export const objectYaml = async (ref: ObjectRef): Promise<ObjectPayload> => getAgent().call<ObjectPayload>('object.get', { ref });

// ---- one-shot views ----

export interface Snapshot {
    columns: Column[];
    rows: Row[];
    total: number;
}

/** Cell index of a column key in a Row (columns start at 2, after id and version). -1 when the schema lacks it. */
export const colIdx = (columns: readonly Column[], key: string): number => {
    const i = columns.findIndex(c => c.key === key);
    return i < 0 ? -1 : i + 2;
};

/**
 * Reads the first `limit` rows of a view once and closes it: a bounded request/response over the normal view engine (filters and
 * sort are server-side), for lists that are not worth a live subscription. Rejects after `timeoutMs`.
 */
export function snapshotView(query: ViewQuery, limit: number, timeoutMs = 8000): Promise<Snapshot> {
    return new Promise((resolve, reject) => {
        const h = getAgent().subscribeView(query, { offset: 0, limit });
        let settled = false;
        let grace: ReturnType<typeof setTimeout> | undefined;
        const finish = (err?: Error) => {
            if (settled) return;
            settled = true;
            clearTimeout(grace);
            clearTimeout(to);
            stop();
            const rows: Row[] = [];
            for (const id of h.ids) {
                const r = h.rows.get(id);
                if (r) rows.push(r);
            }
            const out = { columns: h.columns.value ?? [], rows, total: h.total.value };
            h.close();
            if (err) reject(err);
            else resolve(out);
        };
        const check = () => {
            if (settled || h.columns.value === null) return;
            const want = Math.min(h.total.value, limit);
            if (h.ids.length >= want && (want > 0 || h.version.value > 1)) finish();
            else if (!grace) grace = setTimeout(() => finish(), 1200);
        };
        const stop = watch([h.version, h.total, h.columns], check, { flush: 'sync' });
        const to = setTimeout(() => finish(new Error('timed out reading the report list')), timeoutMs);
        check();
    });
}

export function reportView(
    gvr: string,
    filters: ViewQuery['filters'],
    opts: { clusters?: string[]; namespaces?: string[]; sort?: ViewQuery['sort'] } = {}
): ViewQuery {
    return { clusters: opts.clusters ?? [], gvr, namespaces: opts.namespaces ?? [], text: '', filters, sort: opts.sort ?? [] };
}

/** First available kind of a group with the given plural (`vulnerabilityreports`), or ''. Several served versions: the first listed wins. */
export function gvrOf(kinds: readonly Kind[], group: string, plural: string): string {
    return kinds.find(k => k.available && k.group === group && k.plural === plural)?.gvr ?? '';
}

export const TRIVY_GROUP = 'aquasecurity.github.io';
export const KUBESCAPE_GROUP = 'spdx.softwarecomposition.kubescape.io';

/** Report kinds by purpose and scanner. */
export function reportGvrs(kinds: readonly Kind[]) {
    return {
        trivyVuln: gvrOf(kinds, TRIVY_GROUP, 'vulnerabilityreports'),
        kubescapeVuln: gvrOf(kinds, KUBESCAPE_GROUP, 'vulnerabilitymanifestsummaries'),
        trivyConfig: gvrOf(kinds, TRIVY_GROUP, 'configauditreports'),
        kubescapeConfig: gvrOf(kinds, KUBESCAPE_GROUP, 'workloadconfigurationscansummaries'),
        trivySecret: gvrOf(kinds, TRIVY_GROUP, 'exposedsecretreports')
    };
}

// ---- actions ----

/** `trivy.rescan`: deletes the report so the operator recreates it. Confirms first; resolves true when it ran. */
export async function rescanReport(ref: ObjectRef, label: string): Promise<boolean> {
    const ui = useUiStore();
    const clusters = useClustersStore();
    const name = clusters.nameById.get(ref.cluster) ?? ref.cluster;
    const ok = await ask({
        title: `Rescan ${label}?`,
        body: `Deletes the report ${ref.namespace ? ref.namespace + '/' : ''}${ref.name} on ${name}. The Trivy operator recreates it on its next reconcile, so the report is missing until then.`,
        confirmLabel: 'Delete report',
        hold: true,
        phrase: clusters.isProduction(ref.cluster) ? ref.name : undefined,
        note: clusters.isProduction(ref.cluster) ? 'Production cluster: type the report name to confirm.' : undefined
    });
    if (!ok) return false;
    try {
        const res = await callMutating<{ usedContext: string }>(ref.cluster, 'trivy.rescan', { ref });
        ui.toast('success', `Deleted ${ref.name}${res.usedContext ? ` as ${res.usedContext}` : ''}; the operator will rescan`);
        return true;
    } catch (e) {
        if (!isCancelled(e)) ui.toast('danger', errorText(e));
        return false;
    }
}

/** `kubescape.scanNow` for one cluster (confirm, then write-context handling like other actions). */
export async function kubescapeScanNow(cluster: string): Promise<boolean> {
    const ui = useUiStore();
    const clusters = useClustersStore();
    const name = clusters.nameById.get(cluster) ?? cluster;
    const ok = await ask({
        title: `Run a Kubescape scan on ${name}?`,
        body: `Creates a Job from each of the Kubescape operator's scan CronJobs on ${name}. The scan uses cluster resources while it runs.`,
        confirmLabel: 'Scan now',
        phrase: clusters.isProduction(cluster) ? name : undefined,
        note: clusters.isProduction(cluster) ? 'Production cluster: type the cluster name to confirm.' : undefined
    });
    if (!ok) return false;
    try {
        const params: KubescapeScanNowParams = { cluster };
        const res = await callMutating<{ usedContext: string; jobs: string[] }>(cluster, 'kubescape.scanNow', { ...params });
        const n = res.jobs?.length ?? 0;
        ui.toast(
            'success',
            `${sourceLabel('kubescape')} scan started on ${name}: ${n} job${n === 1 ? '' : 's'}${res.usedContext ? ` as ${res.usedContext}` : ''}`
        );
        return true;
    } catch (e) {
        if (!isCancelled(e)) ui.toast('danger', errorText(e));
        return false;
    }
}

export interface ReportItem {
    ref: ObjectRef;
    key: string;
    /** Every schema column of the row (except cluster/name/namespace) as text, by column key. */
    cells: Record<string, string>;
}

/** Report objects of a snapshot as refs (`security.reportDetail` / `trivy.rescan` take these) plus their cells. */
export function readItems(snap: Snapshot, gvr: string, fallbackCluster: string): ReportItem[] {
    const ci = colIdx(snap.columns, 'cluster');
    const ni = colIdx(snap.columns, 'name');
    const si = colIdx(snap.columns, 'namespace');
    return snap.rows.map(r => {
        const cluster = ci >= 0 ? String(r[ci]) : fallbackCluster;
        const name = String(r[ni]);
        const namespace = si >= 0 ? String(r[si]) || undefined : undefined;
        const cells: Record<string, string> = {};
        snap.columns.forEach((c, i) => {
            if (c.key !== 'cluster' && c.key !== 'name' && c.key !== 'namespace') cells[c.key] = String(r[i + 2] ?? '');
        });
        return { ref: { cluster, gvr, namespace, name }, key: `${cluster}/${gvr}/${namespace ?? ''}/${name}`, cells };
    });
}
