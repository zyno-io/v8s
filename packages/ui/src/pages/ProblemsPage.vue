<script setup lang="ts">
import { computed } from 'vue';
import { useRoute } from 'vue-router';

const route = useRoute();
const title = computed(() => String(route.meta.placeholder ?? 'Problems'));
</script>

<template>
    <main class="placeholder">
        <h1>{{ title }}</h1>
        <p class="muted">The {{ title.toLowerCase() }} view lands in a later phase. Use <span class="kbd">:po</span> to return to pods.</p>
    </main>
</template>

<style scoped>
.placeholder {
    padding: 24px 20px;
    flex-grow: 1;
}
h1 {
    font-size: 19px;
    font-weight: 650;
    letter-spacing: -0.01em;
}
</style>
