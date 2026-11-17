import { shallowRef } from 'vue';

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
