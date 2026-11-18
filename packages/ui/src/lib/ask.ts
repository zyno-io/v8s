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
    /** The note is a protected-cluster notice (shows a lock). */
    locked?: boolean;
    runAs?: string;
    /** On a phone the confirm button is a hold-to-confirm (restart, trigger, scale, rollback). */
    hold?: boolean;
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
            locked: opts.locked,
            runAs: opts.runAs,
            hold: opts.hold,
            run: () => resolve(true),
            onCancel: () => resolve(false)
        });
        pump(ui);
    });
}
