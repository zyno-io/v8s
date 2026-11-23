<script setup lang="ts">
/** A small ring with a turning arc: work in flight. Always sits beside a word that says what; it stands still under reduced motion. */
withDefaults(defineProps<{ size?: number }>(), { size: 10 });
</script>

<template>
    <svg class="spin" :width="size" :height="size" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
        <circle cx="8" cy="8" r="6" opacity="0.25" />
        <path d="M8 2a6 6 0 016 6" stroke-linecap="round" />
    </svg>
</template>

<style scoped>
.spin {
    flex: none;
    animation: spin 0.8s linear infinite;
}
@keyframes spin {
    to {
        transform: rotate(360deg);
    }
}
@media (prefers-reduced-motion: reduce) {
    .spin {
        animation: none;
    }
}
</style>
