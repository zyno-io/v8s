import { useUiStore } from '@/stores/ui.ts';

export interface AskOptions {
    title: string;
    body: string;
    confirmLabel: string;
    danger?: boolean;
    /** The user must type this exactly before the button enables. */
    phrase?: string;
    note?: string;
}

/** Opens the shared confirm dialog. Resolves true when the user runs it, false on Cancel, Esc, backdrop or replacement. */
export function ask(opts: AskOptions): Promise<boolean> {
    const ui = useUiStore();
    return new Promise<boolean>(resolve => {
        ui.dialog = {
            type: 'confirm',
            title: opts.title,
            body: opts.body,
            confirmLabel: opts.confirmLabel,
            danger: !!opts.danger,
            phrase: opts.phrase,
            note: opts.note,
            run: () => resolve(true),
            onCancel: () => resolve(false)
        };
    });
}
