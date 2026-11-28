import type { Row } from '@v8s/client';
import type { ShallowRef } from 'vue';

import type { ColPlan } from './columns.ts';

import { fluxDecor } from './flux-decor.ts';
import { helmDecor } from './helm-decor.ts';
import { securityDecor } from './security-decor.ts';

export type Tone = 'success' | 'warning' | 'danger' | 'info' | 'release' | 'neutral';

/** What a decorated cell shows instead of the default rendering: an optional tag, then optional text. */
export interface CellContent {
    tag?: { text: string; tone: Tone };
    text?: string;
    mono?: boolean;
    muted?: boolean;
    title?: string;
}

export interface ExtraCol {
    key: string;
    label: string;
    width: number;
    mono?: boolean;
}

/**
 * Per-kind customization of the shared ResourceTable (Flux, Helm). Everything here must be cheap and pure per row: it runs for the
 * visible slice only, on every view version bump. External state that a row's version does not capture (a pending reconcile, a
 * lazily fetched chart version) is signaled by bumping `version` and returned from `rowKey` so the row's `v-memo` re-renders.
 */
export interface TableDecor {
    /** Virtual columns appended after the schema columns (cells come from `cell`; they have `idx < 0`). */
    extraCols?: ExtraCol[];
    /** Column keys hidden until the user changes the layout. */
    defaultHidden?: string[];
    /** Replacement content for a cell (`col.key`), or null for the default rendering. */
    cell?(col: ColPlan, row: Row): CellContent | null;
    /** Extra memo key for state outside the row. */
    rowKey?(row: Row): number | string;
    /** Bumped when external state changes, so the slice recomputes. */
    version?: ShallowRef<number>;
    /** The loaded rows currently on screen (debounced ~200 ms): lazy detail fetches hang off this. */
    visible?(rows: readonly Row[]): void;
}

/** Decor for a resource kind, or null. */
export function decorFor(gvr: string): TableDecor | null {
    return fluxDecor(gvr) ?? helmDecor(gvr) ?? securityDecor(gvr);
}
