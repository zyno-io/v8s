import { onBeforeUnmount, shallowRef } from 'vue';

const mq = typeof matchMedia === 'function' ? matchMedia('(max-width: 639.98px)') : null;

/** True below 640px: fixed 72px cards and a ±60 row window instead of 32px rows and ±1000. */
export const isPhone = shallowRef(mq?.matches ?? false);
mq?.addEventListener('change', e => {
    isPhone.value = e.matches;
});

export const ROW_H_DESKTOP = 32;
export const ROW_H_PHONE = 72;
export const WINDOW_PAD_DESKTOP = 1000;
export const WINDOW_PAD_PHONE = 60;

export const rowHeight = () => (isPhone.value ? ROW_H_PHONE : ROW_H_DESKTOP);
export const windowPad = () => (isPhone.value ? WINDOW_PAD_PHONE : WINDOW_PAD_DESKTOP);

/**
 * False once the tab has been hidden for `graceMs`, true again the moment it is shown. Polling that only matters to a visible
 * page (metrics.k8s.io usage) hangs off it, so a tab left in the background stops costing the cluster anything.
 */
export function usePageVisible(graceMs = 30_000) {
    const visible = shallowRef(typeof document === 'undefined' || !document.hidden);
    let timer: ReturnType<typeof setTimeout> | undefined;
    const onChange = () => {
        clearTimeout(timer);
        if (!document.hidden) visible.value = true;
        else timer = setTimeout(() => (visible.value = false), graceMs);
    };
    if (typeof document !== 'undefined') {
        document.addEventListener('visibilitychange', onChange);
        onBeforeUnmount(() => {
            clearTimeout(timer);
            document.removeEventListener('visibilitychange', onChange);
        });
    }
    return visible;
}
