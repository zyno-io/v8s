<script setup lang="ts">
import { useUiStore } from '@/stores/ui.ts';

const ui = useUiStore();
</script>

<template>
    <div class="toasts" role="region" aria-label="Notifications" aria-live="polite">
        <div v-for="t in ui.toasts" :key="t.id" class="toast floating" :class="t.tone" :role="t.tone === 'danger' ? 'alert' : 'status'">
            <span class="msg">{{ t.text }}</span>
            <button class="btn ghost sm" type="button" aria-label="Dismiss" @click="ui.dismissToast(t.id)">×</button>
        </div>
    </div>
</template>

<style scoped>
.toasts {
    position: fixed;
    top: 54px;
    left: 50%;
    transform: translateX(-50%);
    z-index: 90;
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: min(420px, calc(100vw - 32px));
    pointer-events: none;
}
.toast {
    pointer-events: auto;
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 10px 12px;
    font-size: 13px;
    border-left: 3px solid var(--neutral);
}
.toast.success {
    border-left-color: var(--success);
}
.toast.danger {
    border-left-color: var(--danger);
}
.toast.warning {
    border-left-color: var(--warning);
}
.toast.info {
    border-left-color: var(--info);
}
.msg {
    flex-grow: 1;
    word-break: break-word;
}
</style>
