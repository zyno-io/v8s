import { defineStore } from 'pinia';
import { ref, shallowRef, watch } from 'vue';

export type ThemeMode = 'system' | 'light' | 'dark';

export type ToastTone = 'success' | 'danger' | 'warning' | 'info';
export interface Toast {
    id: number;
    tone: ToastTone;
    text: string;
}

import type { ObjectRef } from '@v8s/client';

export interface ConfirmDialog {
    type: 'confirm';
    title: string;
    body: string;
    confirmLabel: string;
    danger: boolean;
    /** The user must type this exactly before the button enables. */
    phrase?: string;
    note?: string;
    run: () => Promise<void> | void;
}

export interface ScaleDialog {
    type: 'scale';
    targets: ObjectRef[];
    current: number;
    run: (replicas: number) => Promise<void> | void;
}

export interface RollbackDialog {
    type: 'rollback';
    target: ObjectRef;
    current: number;
    run: (revision: number) => Promise<void> | void;
}

export type DialogState = ConfirmDialog | ScaleDialog | RollbackDialog;

function loadTheme(): ThemeMode {
    try {
        const v = localStorage.getItem('v8s:theme');
        return v === 'light' || v === 'dark' ? v : 'system';
    } catch {
        return 'system';
    }
}

function applyTheme(mode: ThemeMode) {
    const cl = document.documentElement.classList;
    cl.remove('light', 'dark');
    if (mode !== 'system') cl.add(mode);
    try {
        if (mode === 'system') localStorage.removeItem('v8s:theme');
        else localStorage.setItem('v8s:theme', mode);
    } catch {
        // ignore: preference just will not persist
    }
}

let toastId = 1;

/** UI state only. Datasets never live in Pinia. */
export const useUiStore = defineStore('ui', () => {
    const theme = ref<ThemeMode>(loadTheme());
    const commandBarOpen = ref(false);
    const shortcutsOpen = ref(false);
    const paletteOpen = ref(false);
    const createOpen = ref(false);
    const dialog = shallowRef<DialogState | null>(null);
    const toasts = shallowRef<Toast[]>([]);

    watch(theme, applyTheme);

    function cycleTheme() {
        theme.value = theme.value === 'system' ? 'light' : theme.value === 'light' ? 'dark' : 'system';
    }

    function dismissToast(id: number) {
        toasts.value = toasts.value.filter(t => t.id !== id);
    }

    function toast(tone: ToastTone, text: string, ms = tone === 'danger' ? 9000 : 4500) {
        const id = toastId++;
        toasts.value = [...toasts.value.slice(-3), { id, tone, text }];
        setTimeout(() => dismissToast(id), ms);
    }

    return { theme, commandBarOpen, shortcutsOpen, paletteOpen, createOpen, dialog, toasts, cycleTheme, toast, dismissToast };
});
