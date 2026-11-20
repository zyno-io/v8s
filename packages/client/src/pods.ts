import type { Row } from './types.ts';

/** Cell indexes (in a Row) of the "v1/pods" view, following `PodColumns` in the Go protocol package. */
export const POD = {
    cluster: 2,
    name: 3,
    namespace: 4,
    ready: 5,
    containers: 6,
    status: 7,
    statusTone: 8,
    restarts: 9,
    vpa: 10,
    cpuUse: 11,
    cpuReq: 12,
    cpuLim: 13,
    cpuPctReq: 14,
    cpuPctLim: 15,
    cpuPctNode: 16,
    memUse: 17,
    memReq: 18,
    memLim: 19,
    memPctReq: 20,
    memPctLim: 21,
    memPctNode: 22,
    node: 23,
    ip: 24,
    createdAt: 25
} as const;

export const POD_COLUMN_KEYS = [
    'cluster',
    'name',
    'namespace',
    'ready',
    'containers',
    'status',
    'statusTone',
    'restarts',
    'vpa',
    'cpuUse',
    'cpuReq',
    'cpuLim',
    'cpuPctReq',
    'cpuPctLim',
    'cpuPctNode',
    'memUse',
    'memReq',
    'memLim',
    'memPctReq',
    'memPctLim',
    'memPctNode',
    'node',
    'ip',
    'createdAt'
] as const;

/** Status tone codes (ColTone). */
export const TONE = { neutral: 0, success: 1, warning: 2, danger: 3, info: 4, release: 5 } as const;
export const TONE_NAMES = ['neutral', 'success', 'warning', 'danger', 'info', 'release'] as const;

export const podCell = <T extends string | number>(row: Row, col: number): T => row[col] as T;
