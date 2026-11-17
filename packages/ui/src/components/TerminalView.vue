<script setup lang="ts">
import type { ShellHandle } from '@v8s/client';
import type { IDisposable } from '@xterm/xterm';

import { FitAddon } from '@xterm/addon-fit';
import { Terminal } from '@xterm/xterm';
import { onBeforeUnmount, onMounted, ref, useTemplateRef, watch } from 'vue';
import '@xterm/xterm/css/xterm.css';

import { ACCESSORY_KEYS, applyCtrl, TERMINAL_FONT, TERMINAL_THEME, terminalSizing } from '@/lib/shell.ts';

const props = defineProps<{ handle: ShellHandle; simplified?: boolean }>();

const host = useTemplateRef<HTMLDivElement>('host');
const ctrlOn = ref(false);

let term: Terminal | null = null;
let fit: FitAddon | null = null;
let webgl: IDisposable | null = null;
let ro: ResizeObserver | null = null;
let timer: ReturnType<typeof setTimeout> | undefined;
let disposed = false;
const subs: IDisposable[] = [];
let boundHandle: ShellHandle | null = null;

const encoder = new TextEncoder();
const RED = encoder.encode('\x1b[31m');
const RESET = encoder.encode('\x1b[0m');

function sendSize() {
    if (!term || !fit || !boundHandle) return;
    try {
        fit.fit();
    } catch {
        return;
    }
    boundHandle.resize(term.cols, term.rows);
}

function scheduleFit() {
    clearTimeout(timer);
    timer = setTimeout(sendSize, 50);
}

function bind(h: ShellHandle) {
    if (boundHandle && boundHandle !== h) boundHandle.onData = null;
    boundHandle = h;
    h.onData = (bytes, stderr) => {
        if (!term) return;
        if (stderr) {
            term.write(RED);
            term.write(bytes);
            term.write(RESET);
        } else {
            term.write(bytes);
        }
    };
}

function send(data: string) {
    const { out, consumed } = applyCtrl(data, ctrlOn.value);
    if (consumed) ctrlOn.value = false;
    boundHandle?.write(out);
}

async function copySelection() {
    const sel = term?.getSelection();
    if (!sel) return;
    try {
        await navigator.clipboard.writeText(sel);
    } catch {
        /* clipboard unavailable */
    }
}

async function pasteClipboard() {
    try {
        const text = await navigator.clipboard.readText();
        if (text) term?.paste(text);
    } catch {
        /* clipboard unavailable */
    }
}

async function loadWebgl(t: Terminal) {
    try {
        const { WebglAddon } = await import('@xterm/addon-webgl');
        if (disposed) return;
        const addon = new WebglAddon();
        addon.onContextLoss(() => {
            addon.dispose();
            if (webgl === addon) webgl = null;
        });
        t.loadAddon(addon);
        webgl = addon;
    } catch {
        webgl = null; // no WebGL: keep the default renderer
    }
}

onMounted(() => {
    const el = host.value;
    if (!el) return;
    const sz = terminalSizing(!!props.simplified);
    const t = new Terminal({
        scrollback: sz.scrollback,
        fontFamily: TERMINAL_FONT,
        fontSize: sz.fontSize,
        cursorBlink: true,
        screenReaderMode: sz.screenReaderMode,
        allowProposedApi: true,
        theme: { ...TERMINAL_THEME }
    });
    term = t;
    fit = new FitAddon();
    t.loadAddon(fit);
    t.open(el);
    // Text read-back for tests and assistive tooling: the WebGL renderer draws to a canvas, so the DOM has no text to query.
    Object.defineProperty(el, 'v8sText', {
        configurable: true,
        get() {
            const b = t.buffer.active;
            const lines: string[] = [];
            for (let i = 0; i < b.length; i++) lines.push(b.getLine(i)?.translateToString(true) ?? '');
            return lines.join('\n');
        }
    });
    bind(props.handle);

    subs.push(t.onData(d => send(d)));
    subs.push(
        t.onSelectionChange(() => {
            if (t.hasSelection()) void copySelection();
        })
    );
    t.attachCustomKeyEventHandler(ev => {
        if (ev.type === 'keydown' && ev.shiftKey && (ev.ctrlKey || ev.metaKey)) {
            if (ev.code === 'KeyC') {
                void copySelection();
                return false;
            }
            if (ev.code === 'KeyV') {
                ev.preventDefault();
                void pasteClipboard();
                return false;
            }
        }
        return true;
    });

    ro = new ResizeObserver(scheduleFit);
    ro.observe(el);
    sendSize();
    t.focus();
    if (sz.webgl) void loadWebgl(t);
});

watch(
    () => props.handle.state.value,
    s => {
        if (s === 'ready') sendSize();
    },
    { immediate: true }
);

watch(
    () => props.handle,
    h => {
        bind(h);
        sendSize();
    }
);

onBeforeUnmount(() => {
    disposed = true;
    clearTimeout(timer);
    ro?.disconnect();
    ro = null;
    if (boundHandle) boundHandle.onData = null;
    boundHandle = null;
    for (const s of subs) s.dispose();
    subs.length = 0;
    try {
        webgl?.dispose();
    } catch {
        /* already disposed */
    }
    webgl = null;
    try {
        fit?.dispose();
    } catch {
        /* ignore */
    }
    fit = null;
    term?.dispose();
    term = null;
});

// Accessory keys: pointerdown keeps terminal focus (the soft keyboard stays open); click handles keyboard activation.
let pointerHandled = false;
function press(key: (typeof ACCESSORY_KEYS)[number]) {
    if (key.seq === undefined) ctrlOn.value = !ctrlOn.value;
    else boundHandle?.write(key.seq);
    term?.focus();
}
function onPointerDown(e: PointerEvent, key: (typeof ACCESSORY_KEYS)[number]) {
    e.preventDefault();
    pointerHandled = true;
    press(key);
}
function onClick(key: (typeof ACCESSORY_KEYS)[number]) {
    if (pointerHandled) {
        pointerHandled = false;
        return;
    }
    press(key);
}

defineExpose({ focus: () => term?.focus() });
</script>

<template>
    <div class="tv">
        <p v-if="simplified" class="muted tv-note" role="note">Best on a desktop</p>
        <div ref="host" class="tv-host" data-testid="terminal"></div>
        <div v-if="simplified" class="tv-keys" role="toolbar" aria-label="Terminal keys">
            <button
                v-for="k in ACCESSORY_KEYS"
                :key="k.id"
                type="button"
                class="btn tv-key"
                :class="{ primary: k.id === 'ctrl' && ctrlOn }"
                :aria-label="k.aria"
                :aria-pressed="k.id === 'ctrl' ? ctrlOn : undefined"
                @pointerdown="onPointerDown($event, k)"
                @click="onClick(k)"
            >
                {{ k.label }}
            </button>
        </div>
    </div>
</template>

<style scoped>
.tv {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    background: #0c0d10;
}
.tv-host {
    flex: 1;
    min-height: 0;
    padding: 4px 6px;
    background: #0c0d10;
    overflow: hidden;
}
.tv-note {
    color: #9aa0ac;
    margin: 0;
    padding: 4px 8px;
    font-size: 12px;
}
.tv-keys {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    padding: 4px;
    border-top: 1px solid #2a2d36;
}
.tv-key {
    min-width: 44px;
    min-height: 44px;
    flex: 1 0 auto;
    touch-action: manipulation;
    font-family: ui-monospace, Menlo, monospace;
}
</style>
