import type { ObjectRef } from '@v8s/client';

export const DEFAULT_DEBUG_IMAGE = 'busybox:stable';
export const DEBUG_IMAGE_KEY = 'v8s.debugImage';
export const DEFAULT_CONTAINER_ANNOTATION = 'kubectl.kubernetes.io/default-container';

/** Fixed near-black terminal palette: it stays dark in both app themes (a white terminal in light mode reads as broken). */
export const TERMINAL_THEME = {
    background: '#0c0d10',
    foreground: '#d7dae0',
    cursor: '#d7dae0',
    cursorAccent: '#0c0d10',
    selectionBackground: '#3a4660',
    black: '#1b1d23',
    red: '#f0626b',
    green: '#7fd37a',
    yellow: '#e6c15a',
    blue: '#6aa7f5',
    magenta: '#c792ea',
    cyan: '#5fd0d6',
    white: '#d7dae0',
    brightBlack: '#5c6370',
    brightRed: '#ff7b84',
    brightGreen: '#9ae794',
    brightYellow: '#f3d37a',
    brightBlue: '#8bbcff',
    brightMagenta: '#dcaaf8',
    brightCyan: '#82e3e8',
    brightWhite: '#ffffff'
} as const;

export const TERMINAL_FONT = "ui-monospace, 'SF Mono', 'JetBrains Mono', Menlo, Consolas, monospace";

export interface TerminalSizing {
    scrollback: number;
    fontSize: number;
    webgl: boolean;
    screenReaderMode: boolean;
}

export const terminalSizing = (simplified: boolean): TerminalSizing =>
    simplified
        ? { scrollback: 2000, fontSize: 12, webgl: false, screenReaderMode: true }
        : { scrollback: 10000, fontSize: 13, webgl: true, screenReaderMode: false };

type Obj = Record<string, unknown> | null | undefined;
const asObj = (v: unknown): Record<string, unknown> | null => (v && typeof v === 'object' ? (v as Record<string, unknown>) : null);

export interface ContainerChoice {
    names: string[];
    initial: string;
}

/** Non-init containers of a pod; the default is the `default-container` annotation when it names one of them, else the first. */
export function containerChoice(parsed: Obj): ContainerChoice {
    const spec = asObj(parsed?.spec);
    const list = Array.isArray(spec?.containers) ? (spec.containers as unknown[]) : [];
    const names = list.map(c => String(asObj(c)?.name ?? '')).filter(Boolean);
    const ann = asObj(asObj(parsed?.metadata)?.annotations)?.[DEFAULT_CONTAINER_ANNOTATION];
    const initial = typeof ann === 'string' && names.includes(ann) ? ann : (names[0] ?? '');
    return { names, initial };
}

/**
 * Why Connect is disabled, or '' when a shell can be opened. A terminating pod is not blocked: its containers usually still run
 * (often the reason to shell in is to see why it is stuck terminating), and exec fails on its own once they have stopped.
 */
export function connectBlocker(parsed: Obj): string {
    if (!parsed) return 'Pod not loaded yet';
    const phase = String(asObj(parsed.status)?.phase ?? '');
    if (phase && phase !== 'Running') return `Pod is ${phase}, not Running`;
    if (!containerChoice(parsed).names.length) return 'Pod has no containers';
    return '';
}

/** A note shown beside Connect that does not block it ('' for none). */
export function connectNote(parsed: Obj): string {
    return asObj(asObj(parsed)?.metadata)?.deletionTimestamp ? 'Pod is terminating: the shell ends when its containers stop' : '';
}

/** True when an exit looks like the container has no shell (distroless): offer the debug container. */
export function looksLikeNoShell(exit: { code: number; error?: string } | null | undefined): boolean {
    if (!exit) return false;
    const t = (exit.error ?? '').toLowerCase();
    if (!t) return exit.code === 126 || exit.code === 127;
    return /no shell|executable file not found|not found in \$path|no such file or directory|oci runtime exec failed|exec: ["']?\/?(?:bin\/)?(?:ba)?sh/.test(
        t
    );
}

/** Control code for a key typed while the sticky Ctrl is on; null when the key has none. */
export function ctrlCode(key: string): string | null {
    if (key.length !== 1) return null;
    const c = key.toLowerCase();
    if (c >= 'a' && c <= 'z') return String.fromCharCode(c.charCodeAt(0) - 96);
    switch (c) {
        case '[':
            return '\x1b';
        case '\\':
            return '\x1c';
        case ']':
            return '\x1d';
        case '^':
            return '\x1e';
        case '_':
            return '\x1f';
        case '@':
        case ' ':
            return '\x00';
        case '?':
            return '\x7f';
    }
    return null;
}

export interface AccessoryKey {
    id: string;
    label: string;
    /** Absent for the sticky Ctrl toggle. */
    seq?: string;
    aria: string;
}

export const ACCESSORY_KEYS: readonly AccessoryKey[] = [
    { id: 'esc', label: 'Esc', seq: '\x1b', aria: 'Escape' },
    { id: 'tab', label: 'Tab', seq: '\t', aria: 'Tab' },
    { id: 'ctrl', label: 'Ctrl', aria: 'Control (sticky)' },
    { id: 'up', label: '↑', seq: '\x1b[A', aria: 'Arrow up' },
    { id: 'down', label: '↓', seq: '\x1b[B', aria: 'Arrow down' },
    { id: 'right', label: '→', seq: '\x1b[C', aria: 'Arrow right' },
    { id: 'left', label: '←', seq: '\x1b[D', aria: 'Arrow left' },
    { id: 'pipe', label: '|', seq: '|', aria: 'Pipe' },
    { id: 'slash', label: '/', seq: '/', aria: 'Slash' },
    { id: 'dash', label: '-', seq: '-', aria: 'Dash' },
    { id: 'tilde', label: '~', seq: '~', aria: 'Tilde' }
];

/** Applies the sticky Ctrl to typed data: a single mappable char becomes its control code, anything else passes through. */
export function applyCtrl(data: string, ctrl: boolean): { out: string; consumed: boolean } {
    if (!ctrl) return { out: data, consumed: false };
    const code = ctrlCode(data);
    return code === null ? { out: data, consumed: false } : { out: code, consumed: true };
}

export const execParams = (ref: ObjectRef, container: string) => ({ ref, container, tty: true });
export const debugParams = (ref: ObjectRef, target: string, image: string) => ({ ref, target, image: image.trim() || DEFAULT_DEBUG_IMAGE });
export const nodeShellParams = (cluster: string, node: string) => ({ cluster, node });

/** True when an error message says the cluster's Pod Security admission rejected a privileged pod. */
export const privilegedDenied = (msg: string): boolean => /podsecurity|pod security|privileged/i.test(msg);
