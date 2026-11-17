import type { ErrorBody, ViewHandle } from '@v8s/client';

/** One event of the Events tab (an involved-object events view row). */
export interface EventItem {
    id: number;
    ver: number;
    type: string;
    reason: string;
    message: string;
    count: number;
    lastSeen: number;
    /** The component that reported the event ('' when unknown). */
    source: string;
}

export type EventsView = Pick<ViewHandle, 'columns' | 'version' | 'ids' | 'rows'>;
export type EventsStateView = Pick<ViewHandle, 'columns' | 'version' | 'error'>;

/** The rows of the events view, in the agent's order (newest first), read through the column index map. */
export function eventRows(h: EventsView): EventItem[] {
    void h.version.value; // rows are held non-reactively: depend on the commit counter
    const cols = h.columns.value ?? [];
    const at: Record<string, number> = {};
    cols.forEach((c, i) => (at[c.key] = i + 2));
    const str = (r: readonly unknown[], k: string): string => {
        const v = at[k] === undefined ? undefined : r[at[k]];
        return typeof v === 'string' ? v : '';
    };
    const num = (r: readonly unknown[], k: string): number => {
        const v = at[k] === undefined ? undefined : r[at[k]];
        return typeof v === 'number' ? v : Number(v) || 0;
    };
    const out: EventItem[] = [];
    for (const id of h.ids) {
        const r = h.rows.get(id);
        if (!r) continue;
        out.push({
            id,
            ver: r[1] as number,
            type: str(r, 'type'),
            reason: str(r, 'reason'),
            message: str(r, 'message'),
            count: num(r, 'count'),
            lastSeen: num(r, 'lastSeen'),
            source: str(r, 'source')
        });
    }
    return out;
}

/** Only Warning is highlighted; Normal events stay neutral. */
export function eventTone(type: string): 'warning' | '' {
    return type === 'Warning' ? 'warning' : '';
}

export type EventsState = 'error' | 'loading' | 'empty' | 'list';

/** What the pane shows: the agent answers the subscribe after the first list (or an error), and always sends a first patch, even an empty one. */
export function eventsState(h: EventsStateView, n: number): EventsState {
    if (h.error.value) return 'error';
    if (h.columns.value === null || h.version.value === 0) return 'loading';
    return n > 0 ? 'list' : 'empty';
}

/** Whether trying again can help (a timeout or an agent fault, not a permission or a bad request). */
export function eventsRetryable(e: ErrorBody): boolean {
    return e.code === 'unavailable' || e.code === 'internal';
}

export function eventsErrorText(e: ErrorBody, target: { namespace?: string }): string {
    switch (e.code) {
        case 'forbidden':
            return `You don't have permission to list events in namespace ${target.namespace || 'default'}.`;
        case 'unavailable':
            return `Could not load events: ${e.message}`;
        default:
            return e.message;
    }
}
