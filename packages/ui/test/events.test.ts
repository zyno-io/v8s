import type { Column, ErrorBody } from '@v8s/client';

import { describe, expect, it } from 'vitest';
import { shallowRef } from 'vue';

import { eventRows, eventsErrorText, eventsRetryable, eventsState, eventTone } from '../src/lib/events.ts';

const COLS: Column[] = [
    { key: 'cluster', type: 'dict' },
    { key: 'name', type: 'str' },
    { key: 'namespace', type: 'dict' },
    { key: 'type', type: 'dict' },
    { key: 'reason', type: 'dict' },
    { key: 'object', type: 'str' },
    { key: 'message', type: 'str' },
    { key: 'count', type: 'int' },
    { key: 'lastSeen', type: 'time' },
    { key: 'source', type: 'dict' },
    { key: 'statusTone', type: 'tone' },
    { key: 'createdAt', type: 'time' }
];

// [id, version, cluster, name, namespace, type, reason, object, message, count, lastSeen, source, tone, createdAt]
const row = (id: number, type: string, reason: string, message: string, count: number, lastSeen: number, source: string) =>
    [id, 1, 'c', `e${id}`, 'ns', type, reason, 'Pod/p', message, count, lastSeen, source, 0, 0] as never;

function handle(over: { columns?: Column[] | null; version?: number; error?: ErrorBody | null; rows?: unknown[]; ids?: number[] } = {}) {
    const rows = new Map<number, never>();
    for (const r of (over.rows ?? []) as never[][]) rows.set(r[0] as unknown as number, r as never);
    return {
        columns: shallowRef('columns' in over ? over.columns! : COLS),
        version: shallowRef(over.version ?? 1),
        error: shallowRef<ErrorBody | null>(over.error ?? null),
        ids: new Uint32Array(over.ids ?? []),
        rows: rows as never
    };
}

describe('eventRows', () => {
    it('reads cells through the column index map, newest first as the agent ordered them, including the source', () => {
        const h = handle({
            ids: [2, 1],
            rows: [row(1, 'Warning', 'BackOff', 'boom', 3, 1000, 'kubelet'), row(2, 'Normal', 'Scheduled', 'assigned', 1, 2000, 'default-scheduler')]
        });
        expect(eventRows(h)).toEqual([
            { id: 2, ver: 1, type: 'Normal', reason: 'Scheduled', message: 'assigned', count: 1, lastSeen: 2000, source: 'default-scheduler' },
            { id: 1, ver: 1, type: 'Warning', reason: 'BackOff', message: 'boom', count: 3, lastSeen: 1000, source: 'kubelet' }
        ]);
    });

    it('skips ids whose row is not held yet and tolerates a missing source column', () => {
        const noSource = COLS.filter(c => c.key !== 'source');
        const r = [1, 1, 'c', 'e1', 'ns', 'Normal', 'Started', 'Pod/p', 'ok', 1, 5, 0, 0] as never;
        const h = handle({ columns: noSource, ids: [9, 1], rows: [r] });
        const items = eventRows(h);
        expect(items).toHaveLength(1);
        expect(items[0]).toMatchObject({ reason: 'Started', source: '' });
    });
});

describe('eventTone', () => {
    it('highlights Warning and keeps everything else neutral', () => {
        expect(eventTone('Warning')).toBe('warning');
        expect(eventTone('Normal')).toBe('');
        expect(eventTone('')).toBe('');
    });
});

describe('eventsState', () => {
    it('is loading until the columns and the first patch arrive', () => {
        expect(eventsState(handle({ columns: null, version: 0 }), 0)).toBe('loading');
        expect(eventsState(handle({ version: 0 }), 0)).toBe('loading');
    });
    it('is empty only after the first (empty) patch', () => {
        expect(eventsState(handle({ version: 1 }), 0)).toBe('empty');
    });
    it('lists once there are rows', () => {
        expect(eventsState(handle({ version: 3 }), 2)).toBe('list');
    });
    it('reports an error before anything else', () => {
        expect(eventsState(handle({ columns: [], version: 0, error: { code: 'forbidden', message: 'x' } }), 0)).toBe('error');
    });
});

describe('eventsErrorText', () => {
    it('explains a permission error with the namespace the events live in', () => {
        expect(eventsErrorText({ code: 'forbidden', message: 'x' }, { namespace: 'team-a' })).toBe(
            "You don't have permission to list events in namespace team-a."
        );
        expect(eventsErrorText({ code: 'forbidden', message: 'x' }, {})).toBe("You don't have permission to list events in namespace default.");
    });
    it('prefixes an unavailable error and passes others through', () => {
        expect(eventsErrorText({ code: 'unavailable', message: 'timed out' }, {})).toBe('Could not load events: timed out');
        expect(eventsErrorText({ code: 'not_found', message: 'cluster is not selected' }, {})).toBe('cluster is not selected');
    });
    it('offers a retry for unavailable and internal errors only', () => {
        expect(eventsRetryable({ code: 'unavailable', message: '' })).toBe(true);
        expect(eventsRetryable({ code: 'internal', message: '' })).toBe(true);
        expect(eventsRetryable({ code: 'forbidden', message: '' })).toBe(false);
        expect(eventsRetryable({ code: 'bad_request', message: '' })).toBe(false);
    });
});
