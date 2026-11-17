import type { Row } from '@v8s/client';

import type { ColPlan } from './columns.ts';
import type { CellContent, TableDecor } from './table-decor.ts';

import {
    baseOf,
    cellStr,
    FLUX_BASE,
    isFluxGvr,
    objectKey,
    pendingReconciles,
    pendingToken,
    readCells,
    readyTagOf,
    reconcileState,
    settle,
    shortRevision,
    truncateMessage,
    version
} from './flux.ts';

/** Last known row index of the `cluster` column (set from the plan whenever a cell is decorated). */
let lastBase = FLUX_BASE;

const DASH: CellContent = { text: '—', muted: true };
const SUSPENDED: CellContent = { tag: { text: 'Suspended', tone: 'neutral' } };

export function fluxDecor(gvr: string): TableDecor | null {
    if (!isFluxGvr(gvr)) return null;
    const keyOf = (row: Row, base: number) =>
        objectKey({ cluster: cellStr(row, base, 'cluster'), gvr, namespace: cellStr(row, base, 'namespace'), name: cellStr(row, base, 'name') });

    return {
        defaultHidden: ['requestedAt', 'lastHandled', 'dependsOn'],
        version,
        cell(col: ColPlan, row: Row): CellContent | null {
            const base = baseOf(col.key, col.idx);
            lastBase = base;
            switch (col.key) {
                case 'ready': {
                    const c = readCells(row, base);
                    const token = pendingReconciles.size ? pendingToken(keyOf(row, base)) : undefined;
                    if (!c.suspended && reconcileState(row, token, base) === 'reconciling') return { tag: { text: 'Reconciling…', tone: 'warning' } };
                    const t = readyTagOf(c);
                    return { tag: t, title: c.message || undefined };
                }
                case 'suspended':
                    return cellStr(row, base, 'suspended') === 'true' ? SUSPENDED : DASH;
                case 'revision': {
                    const full = cellStr(row, base, 'revision');
                    return full ? { text: shortRevision(full), mono: true, title: full } : DASH;
                }
                case 'message': {
                    const full = cellStr(row, base, 'message');
                    return full ? { text: truncateMessage(full), title: full } : DASH;
                }
                case 'source':
                case 'interval': {
                    const v = cellStr(row, base, col.key);
                    return v ? { text: v, mono: col.key === 'interval' } : DASH;
                }
                default:
                    return null;
            }
        },
        rowKey(row: Row): number {
            if (!pendingReconciles.size) return 0;
            return pendingToken(keyOf(row, lastBase)) ? 1 : 0;
        },
        visible(rows: readonly Row[]) {
            if (!pendingReconciles.size) return;
            for (const row of rows) settle(keyOf(row, lastBase), cellStr(row, lastBase, 'lastHandled'));
        }
    };
}
