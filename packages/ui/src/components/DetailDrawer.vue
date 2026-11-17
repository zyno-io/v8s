<script setup lang="ts">
import { nextTick, ref, watch } from 'vue';

import { clusterColor } from '@/lib/colors.ts';
import { useUiStore } from '@/stores/ui.ts';

const ui = useUiStore();
const closeBtn = ref<HTMLButtonElement | null>(null);

watch(
    () => ui.drawer?.id,
    async id => {
        if (id) {
            await nextTick();
            closeBtn.value?.focus();
        }
    }
);
</script>

<template>
    <aside v-if="ui.drawer" class="drawer floating" role="complementary" aria-label="Pod details">
        <header>
            <h2 class="c">{{ ui.drawer.name }}</h2>
            <button ref="closeBtn" class="btn ghost sm" type="button" aria-label="Close details" @click="ui.drawer = null">Close</button>
        </header>
        <dl>
            <dt class="lbl">Name</dt>
            <dd class="mono">{{ ui.drawer.name }}</dd>
            <dt class="lbl">Cluster</dt>
            <dd>
                <span class="tag"><span class="dot" :style="{ background: clusterColor(ui.drawer.cluster) }"></span>{{ ui.drawer.cluster }}</span>
            </dd>
            <dt class="lbl">Namespace</dt>
            <dd>{{ ui.drawer.namespace }}</dd>
        </dl>
        <p class="muted note">Details, YAML, events and logs arrive in a later phase.</p>
    </aside>
</template>

<style scoped>
.drawer {
    position: fixed;
    z-index: 35;
    top: 58px;
    right: 12px;
    bottom: 12px;
    width: min(560px, calc(100vw - 24px));
    padding: 16px 18px;
    overflow: auto;
    border-radius: 12px;
}
header {
    display: flex;
    align-items: center;
    gap: 12px;
    justify-content: space-between;
}
h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
}
dl {
    display: grid;
    grid-template-columns: 96px 1fr;
    gap: 10px 12px;
    margin: 18px 0 0;
    align-items: center;
}
dt,
dd {
    margin: 0;
}
.note {
    margin-top: 24px;
    font-size: 12px;
}
</style>
