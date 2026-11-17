import { describe, expect, it } from 'vitest';

import {
    colorForSource,
    containersOf,
    cssColor,
    defaultContainer,
    downloadFilename,
    downloadParams,
    dropCompensation,
    formatNewLines,
    hasPrevious,
    isAtBottom,
    logTargetKind,
    newLinesSince,
    paletteVar,
    rowHeight,
    runStyle,
    shortTimestamp,
    shouldPauseFollow,
    splitRuns,
    streamParams,
    viewRange
} from '../src/lib/logs.ts';

const pod = {
    metadata: { annotations: {} as Record<string, string> },
    spec: { containers: [{ name: 'app' }, { name: 'sidecar' }], initContainers: [{ name: 'init' }], ephemeralContainers: [{ name: 'dbg' }] },
    status: {
        containerStatuses: [
            { name: 'app', restartCount: 0 },
            { name: 'sidecar', restartCount: 3 }
        ],
        initContainerStatuses: [{ name: 'init', restartCount: 0 }]
    }
};

describe('containers', () => {
    it('lists containers with init flag and restarts', () => {
        expect(containersOf(pod)).toEqual([
            { name: 'app', init: false, restarts: 0 },
            { name: 'sidecar', init: false, restarts: 3 },
            { name: 'init', init: true, restarts: 0 },
            { name: 'dbg', init: false, ephemeral: true, restarts: 0 }
        ]);
        expect(containersOf(null)).toEqual([]);
    });
    it('picks the first non-init container, honouring the annotation', () => {
        expect(defaultContainer(pod)).toBe('app');
        const ann = { ...pod, metadata: { annotations: { 'kubectl.kubernetes.io/default-container': 'sidecar' } } };
        expect(defaultContainer(ann)).toBe('sidecar');
        const bad = { ...pod, metadata: { annotations: { 'kubectl.kubernetes.io/default-container': 'nope' } } };
        expect(defaultContainer(bad)).toBe('app');
        expect(defaultContainer(null)).toBe('');
    });
    it('hasPrevious needs restarts', () => {
        const l = containersOf(pod);
        expect(hasPrevious(l, 'app')).toBe(false);
        expect(hasPrevious(l, 'sidecar')).toBe(true);
        expect(hasPrevious(l, 'missing')).toBe(false);
    });
});

describe('streamParams', () => {
    const podRef = { cluster: 'c1', gvr: 'v1/pods', namespace: 'ns', name: 'p' };
    it('builds a pod stream', () => {
        expect(streamParams(podRef, { container: 'app', follow: true, tailLines: 1000, phone: false })).toEqual({
            ref: podRef,
            container: 'app',
            timestamps: true,
            follow: true,
            tailLines: 1000,
            capacity: 50000,
            multi: false
        });
    });
    it('previous disables follow; phone shrinks capacity', () => {
        const p = streamParams(podRef, { container: 'app', follow: true, tailLines: 5000, previous: true, phone: true });
        expect(p.previous).toBe(true);
        expect(p.follow).toBe(false);
        expect(p.capacity).toBe(5000);
    });
    it('workloads are merged with no container', () => {
        const d = { cluster: 'c1', gvr: 'apps/v1/deployments', namespace: 'ns', name: 'web' };
        const p = streamParams(d, { container: 'x', follow: true, tailLines: 1000, previous: true, phone: false });
        expect(p.multi).toBe(true);
        expect(p.container).toBeUndefined();
        expect(p.previous).toBeUndefined();
    });
    it('download params and file names', () => {
        const p = downloadParams(podRef, { container: 'app' });
        expect(p).toMatchObject({ ref: podRef, container: 'app', follow: false });
        expect(p).not.toHaveProperty('capacity');
        expect(downloadFilename(podRef, 'app')).toBe('p-app.log');
        expect(downloadFilename({ ...podRef, gvr: 'apps/v1/deployments', name: 'web' }, 'app')).toBe('web.log');
    });
    it('target kinds', () => {
        expect(logTargetKind('v1/pods')).toBe('pod');
        expect(logTargetKind('apps/v1/statefulsets')).toBe('workload');
        expect(logTargetKind('v1/services')).toBe('workload');
        expect(logTargetKind('v1/configmaps')).toBeNull();
    });
});

describe('splitRuns', () => {
    it('is one run without spans or hits', () => {
        expect(splitRuns('hello')).toEqual([{ text: 'hello', fg: -1, bg: -1, flags: 0, hl: false }]);
    });
    it('splits by spans', () => {
        const r = splitRuns('abcdef', [1, 3, 1, -1, 1]);
        expect(r.map(x => [x.text, x.fg, x.flags])).toEqual([
            ['a', -1, 0],
            ['bc', 1, 1],
            ['def', -1, 0]
        ]);
    });
    it('splits by highlights and combines them with spans', () => {
        const r = splitRuns('abcdef', [0, 4, 2, -1, 0], [2, 5]);
        expect(r.map(x => [x.text, x.fg, x.hl])).toEqual([
            ['ab', 2, false],
            ['cd', 2, true],
            ['e', -1, true],
            ['f', -1, false]
        ]);
        expect(r.map(x => x.text).join('')).toBe('abcdef');
    });
    it('clamps out-of-range offsets', () => {
        expect(splitRuns('ab', undefined, [1, 9]).map(x => x.text)).toEqual(['a', 'b']);
    });
});

describe('colours', () => {
    it('maps palette and rgb', () => {
        expect(paletteVar(3)).toBe('--ansi-3');
        expect(cssColor(3)).toBe('var(--ansi-3)');
        expect(cssColor(0x1000000 | 0x12ab00)).toBe('#12ab00');
        expect(cssColor(-1)).toBe('');
        expect(runStyle({ text: 'x', fg: 1, bg: -1, flags: 9, hl: false })).toBe('color:var(--ansi-1);font-weight:700;text-decoration:underline;');
    });
    it('source colour is stable, in range, and shared by containers of a pod', () => {
        const a = colorForSource('web-1/app');
        expect(a).toBe(colorForSource('web-1/app'));
        expect(a).toBe(colorForSource('web-1/sidecar'));
        for (const n of ['a/b', 'web-2/x', 'zzz/q']) expect(colorForSource(n)).toBeGreaterThanOrEqual(0);
        expect(colorForSource('web-2/x')).toBeLessThan(8);
    });
});

describe('follow helpers', () => {
    it('detects the bottom with tolerance', () => {
        expect(isAtBottom(900, 100, 1000)).toBe(true);
        expect(isAtBottom(896, 100, 1000)).toBe(true);
        expect(isAtBottom(890, 100, 1000)).toBe(false);
    });
    it('pauses only on an upward move away from the bottom', () => {
        expect(shouldPauseFollow(900, 800, 100, 1000)).toBe(true);
        expect(shouldPauseFollow(900, 899, 100, 1000)).toBe(false);
        expect(shouldPauseFollow(500, 600, 100, 2000)).toBe(false);
    });
    it('compensates for dropped lines', () => {
        expect(dropCompensation(0, 10, 18)).toBe(-180);
        expect(dropCompensation(10, 10, 18)).toBe(0);
    });
    it('formats the pill and counts new lines', () => {
        expect(formatNewLines(1)).toBe('1 new line');
        expect(formatNewLines(1234)).toBe('1,234 new lines');
        expect(newLinesSince(100, 130)).toBe(30);
        expect(newLinesSince(100, 90)).toBe(0);
    });
    it('row heights and view range', () => {
        expect(rowHeight(false)).toBe(18);
        expect(rowHeight(true)).toBe(36);
        expect(viewRange(5, 24, 100, 10)).toEqual({ first: 95, count: 40 });
        expect(viewRange(0, 9, 0, 10).first).toBe(0);
    });
    it('shortens timestamps', () => {
        expect(shortTimestamp('2024-05-01T12:00:00.123456789Z')).toBe('2024-05-01 12:00:00.123');
        expect(shortTimestamp('2024-05-01T12:00:00Z')).toBe('2024-05-01 12:00:00');
        expect(shortTimestamp('weird')).toBe('weird');
    });
});
