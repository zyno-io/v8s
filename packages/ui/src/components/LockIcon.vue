<script setup lang="ts">
/** The lock glyph of the Protected flag: outline when off, filled in the danger tone when on. */
withDefaults(defineProps<{ size?: number; filled?: boolean }>(), { size: 12, filled: true });
</script>

<template>
    <svg
        :width="size"
        :height="size"
        viewBox="0 0 16 16"
        :fill="filled ? 'currentColor' : 'none'"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linejoin="round"
        aria-hidden="true"
    >
        <rect x="3" y="7" width="10" height="7" rx="1.5" />
        <path d="M5.5 7V5a2.5 2.5 0 015 0v2" fill="none" />
    </svg>
</template>
