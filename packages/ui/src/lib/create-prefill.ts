import { shallowRef } from 'vue';

/** YAML handed to the create dialog (e.g. a PolicyException template). The dialog reads and clears it when it opens. */
export interface CreatePrefill {
    yaml: string;
    cluster?: string;
}

const pending = shallowRef<CreatePrefill | null>(null);

/** Stages `p` for the next create dialog and opens it through `open` (set `ui.createOpen = true`). */
export function openCreateWith(p: CreatePrefill, open: () => void): void {
    pending.value = p;
    open();
}

/** Called by CreateDialog when it mounts: returns the staged prefill once. */
export function takePrefill(): CreatePrefill | null {
    const p = pending.value;
    pending.value = null;
    return p;
}
