import type { Row } from '@v8s/client';

import type { ColPlan } from './columns.ts';
import type { CellContent, TableDecor } from './table-decor.ts';

import { isLegacyPolicyGvr, LEGACY_HINT, modeTag } from './policy.ts';
import { sevTone } from './security.ts';

const SEV_COLS = new Set(['critical', 'high', 'medium', 'low']);
const DASH: CellContent = { text: '0', muted: true };

const groupOf = (gvr: string) => gvr.slice(0, Math.max(gvr.indexOf('/'), 0));
const REPORT_GROUPS = new Set(['aquasecurity.github.io', 'spdx.softwarecomposition.kubescape.io']);
const POLICY_REPORT_GROUPS = new Set(['wgpolicyk8s.io', 'openreports.io']);
const POLICY_GROUPS = new Set(['policies.kyverno.io', 'kyverno.io']);

const num = (row: Row, idx: number): number => {
    const v = row[idx];
    return typeof v === 'number' ? v : Number(v) || 0;
};

/**
 * Table decor for the security kinds: severity count cells of Trivy and Kubescape reports colored by severity (`unknown` stays
 * neutral), Kyverno policy `mode` as a tag and the legacy `readOnly` badge, policy report fail/error counts.
 */
export function securityDecor(gvr: string): TableDecor | null {
    const group = groupOf(gvr);
    if (REPORT_GROUPS.has(group))
        return {
            cell(col: ColPlan, row: Row): CellContent | null {
                if (col.key === 'unknown') {
                    const n = num(row, col.idx);
                    return n > 0 ? { tag: { text: String(n), tone: 'neutral' } } : DASH;
                }
                if (!SEV_COLS.has(col.key)) return null;
                const n = num(row, col.idx);
                return n > 0 ? { tag: { text: String(n), tone: sevTone(col.key) } } : DASH;
            }
        };
    if (POLICY_REPORT_GROUPS.has(group))
        return {
            cell(col: ColPlan, row: Row): CellContent | null {
                if (col.key === 'fail' || col.key === 'error') {
                    const n = num(row, col.idx);
                    return n > 0 ? { tag: { text: String(n), tone: 'danger' } } : DASH;
                }
                if (col.key === 'warn') {
                    const n = num(row, col.idx);
                    return n > 0 ? { tag: { text: String(n), tone: 'warning' } } : DASH;
                }
                return null;
            }
        };
    if (POLICY_GROUPS.has(group)) {
        const legacy = isLegacyPolicyGvr(gvr);
        return {
            cell(col: ColPlan, row: Row): CellContent | null {
                if (col.key === 'mode') {
                    const t = modeTag(String(row[col.idx] ?? ''));
                    return t ? { tag: t, title: legacy ? LEGACY_HINT : undefined } : null;
                }
                if (col.key === 'readOnly') {
                    const ro = String(row[col.idx]) === 'true' || legacy;
                    return ro ? { tag: { text: 'Read-only', tone: 'neutral' }, title: LEGACY_HINT } : { text: '—', muted: true };
                }
                return null;
            }
        };
    }
    return null;
}
