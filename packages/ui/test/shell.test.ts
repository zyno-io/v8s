import { describe, expect, it } from 'vitest';

import {
    ACCESSORY_KEYS,
    applyCtrl,
    connectBlocker,
    connectNote,
    containerChoice,
    ctrlCode,
    debugParams,
    DEFAULT_DEBUG_IMAGE,
    execParams,
    looksLikeNoShell,
    nodeShellParams,
    privilegedDenied,
    TERMINAL_THEME,
    terminalSizing
} from '@/lib/shell.ts';

const pod = (over: Record<string, unknown> = {}) => ({
    metadata: { annotations: {} },
    spec: { containers: [{ name: 'app' }, { name: 'side' }], initContainers: [{ name: 'init' }] },
    status: { phase: 'Running' },
    ...over
});

describe('containerChoice', () => {
    it('lists non-init containers, first by default', () => {
        expect(containerChoice(pod())).toEqual({ names: ['app', 'side'], initial: 'app' });
    });
    it('honors the default-container annotation only when valid', () => {
        const a = (v: string) => pod({ metadata: { annotations: { 'kubectl.kubernetes.io/default-container': v } } });
        expect(containerChoice(a('side')).initial).toBe('side');
        expect(containerChoice(a('nope')).initial).toBe('app');
    });
    it('handles null', () => {
        expect(containerChoice(null)).toEqual({ names: [], initial: '' });
    });
});

describe('connectBlocker', () => {
    it('allows running pods', () => expect(connectBlocker(pod())).toBe(''));
    it('blocks others', () => {
        expect(connectBlocker(pod({ status: { phase: 'Pending' } }))).toMatch(/Pending/);
        expect(connectBlocker(null)).not.toBe('');
    });
    it('allows a terminating pod that is still running, with a note', () => {
        const t = pod({ metadata: { deletionTimestamp: 'x' } });
        expect(connectBlocker(t)).toBe('');
        expect(connectNote(t)).toMatch(/terminating/);
        expect(connectNote(pod())).toBe('');
    });
});

describe('looksLikeNoShell', () => {
    it('detects messages', () => {
        expect(looksLikeNoShell({ code: 1, error: 'no shell found in container' })).toBe(true);
        expect(
            looksLikeNoShell({
                code: 1,
                error: 'OCI runtime exec failed: exec failed: unable to start container process: exec: "/bin/bash": stat /bin/bash: no such file or directory'
            })
        ).toBe(true);
        expect(looksLikeNoShell({ code: 1, error: 'executable file not found in $PATH' })).toBe(true);
    });
    it('detects bare 126/127 and rejects others', () => {
        expect(looksLikeNoShell({ code: 127 })).toBe(true);
        expect(looksLikeNoShell({ code: 0 })).toBe(false);
        expect(looksLikeNoShell({ code: 1, error: 'pod deleted' })).toBe(false);
        expect(looksLikeNoShell(null)).toBe(false);
    });
});

describe('ctrlCode', () => {
    it('maps keys', () => {
        expect(ctrlCode('c')).toBe('\x03');
        expect(ctrlCode('C')).toBe('\x03');
        expect(ctrlCode('d')).toBe('\x04');
        expect(ctrlCode('z')).toBe('\x1a');
        expect(ctrlCode('[')).toBe('\x1b');
        expect(ctrlCode('\\')).toBe('\x1c');
        expect(ctrlCode('?')).toBe('\x7f');
        expect(ctrlCode('1')).toBeNull();
        expect(ctrlCode('ab')).toBeNull();
    });
    it('applyCtrl consumes the toggle only when mapped', () => {
        expect(applyCtrl('c', true)).toEqual({ out: '\x03', consumed: true });
        expect(applyCtrl('1', true)).toEqual({ out: '1', consumed: false });
        expect(applyCtrl('c', false)).toEqual({ out: 'c', consumed: false });
    });
});

describe('accessory keys', () => {
    const seq = (id: string) => ACCESSORY_KEYS.find(k => k.id === id)?.seq;
    it('sends the expected sequences', () => {
        expect(seq('esc')).toBe('\x1b');
        expect(seq('tab')).toBe('\t');
        expect(seq('up')).toBe('\x1b[A');
        expect(seq('down')).toBe('\x1b[B');
        expect(seq('right')).toBe('\x1b[C');
        expect(seq('left')).toBe('\x1b[D');
        expect(ACCESSORY_KEYS.find(k => k.id === 'ctrl')?.seq).toBeUndefined();
        expect(ACCESSORY_KEYS.map(k => k.label)).toEqual(expect.arrayContaining(['|', '/', '-', '~']));
    });
});

describe('theme and sizing', () => {
    it('is near-black with light text', () => {
        expect(TERMINAL_THEME.background).toBe('#0c0d10');
        expect(TERMINAL_THEME.foreground).not.toBe(TERMINAL_THEME.background);
    });
    it('phone is simplified', () => {
        expect(terminalSizing(true)).toEqual({ scrollback: 2000, fontSize: 12, webgl: false, screenReaderMode: true });
        expect(terminalSizing(false)).toEqual({ scrollback: 10000, fontSize: 13, webgl: true, screenReaderMode: false });
    });
});

describe('params', () => {
    const ref = { cluster: 'c', gvr: 'v1/pods', namespace: 'ns', name: 'p' };
    it('builds', () => {
        expect(execParams(ref, 'app')).toEqual({ ref, container: 'app', tty: true });
        expect(debugParams(ref, 'app', ' ')).toEqual({ ref, target: 'app', image: DEFAULT_DEBUG_IMAGE });
        expect(debugParams(ref, 'app', 'alpine')).toEqual({ ref, target: 'app', image: 'alpine' });
        expect(nodeShellParams('c', 'n1')).toEqual({ cluster: 'c', node: 'n1' });
    });
});

describe('privilegedDenied', () => {
    it('matches PodSecurity messages', () => {
        expect(privilegedDenied('pods "x" is forbidden: violates PodSecurity "baseline:latest": privileged')).toBe(true);
        expect(privilegedDenied('connection refused')).toBe(false);
    });
});
