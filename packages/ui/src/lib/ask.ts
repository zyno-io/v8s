import { watch } from 'vue';

import type { ConfirmDialog } from '@/stores/ui.ts';

import { useUiStore } from '@/stores/ui.ts';

export interface AskOptions {
    title: string;
    body: string;
    confirmLabel: string;
    danger?: boolean;
    /** The user must type this exactly before the button enables. */
    phrase?: string;
    note?: string;
    runAs?: string;
}

const queue: ConfirmDialog[] = [];
const watched = new WeakSet<object>();

function pump(ui: ReturnType<typeof useUiStore>) {
    if (ui.dialog || !queue.length) return;
    ui.dialog = queue.shift()!;
}

/**
 * Opens the shared confirm dialog; prompts queue behind whatever is open (a bulk action's prompts never replace each other).
 * Resolves true when the user runs it, false on Cancel, Esc, backdrop or replacement.
 */
export function ask(opts: AskOptions): Promise<boolean> {
    const ui = useUiStore();
    if (!watched.has(ui)) {
        watched.add(ui);
        watch(
            () => ui.dialog,
            () => pump(ui),
            { flush: 'sync' }
        );
    }
    return new Promise<boolean>(resolve => {
        queue.push({
            type: 'confirm',
            title: opts.title,
            body: opts.body,
            confirmLabel: opts.confirmLabel,
            danger: !!opts.danger,
            phrase: opts.phrase,
            note: opts.note,
            runAs: opts.runAs,
            run: () => resolve(true),
            onCancel: () => resolve(false)
        });
        pump(ui);
    });
}
