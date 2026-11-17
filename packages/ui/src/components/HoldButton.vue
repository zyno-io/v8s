<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue';

const props = withDefaults(defineProps<{ label: string; disabled?: boolean; ms?: number; danger?: boolean }>(), {
    ms: 1200,
    disabled: false,
    danger: true
});
const emit = defineEmits<{ confirm: [] }>();

/** 0..1 progress of the current hold; drives the fill. */
const progress = ref(0);
let raf = 0;
let start = 0;
let done = false;

function frame(now: number) {
    progress.value = Math.min(1, (now - start) / props.ms);
    if (progress.value >= 1) {
        done = true;
        raf = 0;
        emit('confirm');
        return;
    }
    raf = requestAnimationFrame(frame);
}
function begin() {
    if (props.disabled || raf || done) return;
    start = performance.now();
    raf = requestAnimationFrame(frame);
}
function cancel() {
    if (raf) cancelAnimationFrame(raf);
    raf = 0;
    if (!done) progress.value = 0;
}
function onKeyDown(e: KeyboardEvent) {
    if ((e.key === ' ' || e.key === 'Enter') && !e.repeat) {
        e.preventDefault();
        begin();
    }
}
function onKeyUp(e: KeyboardEvent) {
    if (e.key === ' ' || e.key === 'Enter') cancel();
}
onBeforeUnmount(cancel);
</script>

<template>
    <button
        class="btn hold"
        :class="danger ? 'danger' : 'primary'"
        type="button"
        :disabled="disabled"
        :aria-label="`Hold to confirm: ${label}`"
        data-testid="hold-confirm"
        :style="{ '--p': progress }"
        @pointerdown="begin"
        @pointerup="cancel"
        @pointerleave="cancel"
        @pointercancel="cancel"
        @contextmenu.prevent
        @keydown="onKeyDown"
        @keyup="onKeyUp"
        @blur="cancel"
    >
        <span class="fill" aria-hidden="true"></span>
        <span class="txt">{{ progress > 0 && progress < 1 ? 'Keep holding…' : `Hold to ${label.toLowerCase()}` }}</span>
    </button>
</template>

<style scoped>
.hold {
    position: relative;
    overflow: hidden;
    min-width: 160px;
    touch-action: none;
    user-select: none;
    -webkit-user-select: none;
    -webkit-touch-callout: none;
}
.fill {
    position: absolute;
    inset: 0;
    background: currentColor;
    opacity: 0.22;
    transform-origin: left center;
    transform: scaleX(var(--p));
}
.txt {
    position: relative;
}
</style>
