<script setup lang="ts">
import { nextTick, ref, watch } from 'vue';

import { useUiStore } from '@/stores/ui.ts';

const ui = useUiStore();
const closeBtn = ref<HTMLButtonElement | null>(null);

const groups: { title: string; rows: [string, string][] }[] = [
    {
        title: 'Navigate',
        rows: [
            [':', 'open the command bar'],
            ['/', 'focus the filter'],
            ['j / k', 'move focus down / up'],
            ['Enter', 'open the focused row'],
            ['x', 'select the focused row'],
            ['l', 'open logs of the focused pod or workload'],
            ['s', 'open a shell in the focused pod'],
            ['e', 'edit the YAML of the focused or open object'],
            ['d', 'describe: open the focused object, Overview tab'],
            ['Enter', 'in the filter: move focus to the results'],
            ['Esc', 'clear filter, then selection, then close the drawer'],
            ['Ctrl/⌘ K', 'search objects, kinds and commands'],
            ['[', 'pin or unpin the sidebar'],
            ['?', 'show this list']
        ]
    },
    {
        title: 'Command bar',
        rows: [
            [':ctx', 'choose clusters'],
            [':ctx a b', 'connect exactly a and b'],
            [':ctx +a / -a', 'add / remove a cluster'],
            [':po [ns]', 'pods, optionally in a namespace'],
            [':<type> ns/name', 'open one object in the drawer'],
            [':star / :unstar', 'star or unstar the current type'],
            [':ns <ns> / all', 'set or clear the namespace filter'],
            [':pb', 'problems'],
            [':q', 'close the bar']
        ]
    }
];

watch(
    () => ui.shortcutsOpen,
    async o => {
        if (o) {
            await nextTick();
            closeBtn.value?.focus();
        }
    }
);
</script>

<template>
    <template v-if="ui.shortcutsOpen">
        <div class="backdrop" @mousedown="ui.shortcutsOpen = false"></div>
        <section class="floating box" role="dialog" aria-label="Keyboard shortcuts">
            <header>
                <h2>Keyboard shortcuts</h2>
                <button ref="closeBtn" class="btn ghost sm" type="button" @click="ui.shortcutsOpen = false">Close</button>
            </header>
            <div v-for="g in groups" :key="g.title" class="grp">
                <div class="lbl">{{ g.title }}</div>
                <dl>
                    <template v-for="[k, d] in g.rows" :key="k">
                        <dt><span class="kbd">{{ k }}</span></dt>
                        <dd>{{ d }}</dd>
                    </template>
                </dl>
            </div>
        </section>
    </template>
</template>

<style scoped>
.backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.25);
    z-index: 50;
}
.box {
    position: fixed;
    z-index: 51;
    top: 12vh;
    left: 50%;
    transform: translateX(-50%);
    width: min(560px, calc(100vw - 24px));
    max-height: 76vh;
    overflow: auto;
    padding: 16px 18px;
}
header {
    display: flex;
    align-items: center;
    justify-content: space-between;
}
h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
}
.grp {
    margin-top: 14px;
}
dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 14px;
    margin: 6px 0 0;
    font-size: 13px;
}
dd {
    margin: 0;
    color: var(--text2);
}
dt {
    margin: 0;
}
</style>
