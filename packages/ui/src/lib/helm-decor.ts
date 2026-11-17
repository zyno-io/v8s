import type { HelmRelease, Row } from '@v8s/client';

import { HelmGVR } from '@v8s/client';

import type { ColPlan } from './columns.ts';
import type { CellContent, TableDecor } from './table-decor.ts';

import { getAgent } from './agent.ts';
import { detailOf, HELM_COL, helmStatusTone, parseManagedBy, ReleaseDetails, type ReleaseRef } from './helm.ts';

let details: ReleaseDetails | null = null;
const getDetails = (): ReleaseDetails =>
    (details ??= new ReleaseDetails(async r =>
        detailOf(await getAgent().call<HelmRelease>('helm.release', { cluster: r.cluster, namespace: r.namespace, name: r.name }))
    ));

const refOf = (row: Row): ReleaseRef => ({
    cluster: String(row[HELM_COL.cluster] ?? ''),
    namespace: String(row[HELM_COL.namespace] ?? ''),
    name: String(row[HELM_COL.name] ?? ''),
    revision: Number(row[HELM_COL.revision] ?? 0)
});

const FIELD: Record<string, 'chart' | 'chartVersion' | 'appVersion'> = { chart: 'chart', chartVersion: 'chartVersion', appVersion: 'appVersion' };

/** Table decor for the virtual Helm release view; the view's column order is fixed (see `HELM_COL`). */
export function helmDecor(gvr: string): TableDecor | null {
    if (gvr !== HelmGVR) return null;
    const d = getDetails();
    return {
        extraCols: [
            { key: 'chart', label: 'Chart', width: 150 },
            { key: 'chartVersion', label: 'Chart version', width: 110, mono: true },
            { key: 'appVersion', label: 'App version', width: 100, mono: true }
        ],
        defaultHidden: [],
        version: d.version,
        cell(col: ColPlan, row: Row): CellContent | null {
            if (col.key === 'status') {
                const s = String(row[HELM_COL.status] ?? '');
                return { tag: { text: s, tone: helmStatusTone(s) } };
            }
            if (col.key === 'managedBy') {
                const m = parseManagedBy(String(row[HELM_COL.managedBy] ?? ''));
                return m ? { text: `Flux · ${m.namespace}/${m.name}` } : { text: '—', muted: true };
            }
            const f = FIELD[col.key];
            if (!f) return null;
            const st = d.state(refOf(row));
            if (st === 'loading') return { text: '…', muted: true };
            if (st === 'unknown' || !st[f]) return { text: '—', muted: true };
            return { text: st[f], mono: col.key !== 'chart' };
        },
        rowKey(row: Row) {
            const st = d.state(refOf(row));
            return typeof st === 'string' ? st : `${st.chart}|${st.chartVersion}|${st.appVersion}`;
        },
        visible(rows: readonly Row[]) {
            d.setVisible(rows.map(refOf));
        }
    };
}
