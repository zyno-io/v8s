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
    /** Phones confirm by holding the button (state-changing, not necessarily destructive actions). */
    hold?: boolean;
    /** "Runs as <write context>": which context will perform the action; shown in the dialog body. */
    runAs?: string;
    run: () => Promise<void> | void;
    /** Called when the dialog is dismissed without running (Cancel, Esc, backdrop, replaced). */
    onCancel?: () => void;
}

/** Extras every dialog kind can carry (protected-cluster / bulk confirmation). */
interface DialogExtras {
    phrase?: string;
    note?: string;
    runAs?: string;
}

export interface ScaleDialog extends DialogExtras {
    type: 'scale';
    targets: ObjectRef[];
    /** Current replica count; null when unknown (the field starts empty and is required). */
    current: number | null;
    run: (replicas: number) => Promise<void> | void;
}

export interface RollbackDialog extends DialogExtras {
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
    /** Text the palette starts with (`:ip 10.0.0.5` opens it filled in); consumed on mount. */
    const paletteQuery = ref('');
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

    return { theme, commandBarOpen, shortcutsOpen, paletteOpen, paletteQuery, createOpen, dialog, toasts, cycleTheme, toast, dismissToast };
});
