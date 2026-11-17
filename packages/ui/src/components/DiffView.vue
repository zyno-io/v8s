<script setup lang="ts">
import { computed } from 'vue';

import { collapseContext, countChanges, diffLines } from '@/lib/diff.ts';

const props = defineProps<{ left: string; right: string; leftLabel: string; rightLabel: string }>();

const rows = computed(() => diffLines(props.left, props.right));
const changes = computed(() => countChanges(rows.value));
const blocks = computed(() => collapseContext(rows.value, 3));
</script>

<template>
    <div class="diff card" role="group" :aria-label="`${leftLabel} compared with ${rightLabel}`">
        <div class="cap">{{ leftLabel }}</div>
        <div class="cap r">{{ rightLabel }} <span class="muted n">{{ changes }} line{{ changes === 1 ? '' : 's' }} changed</span></div>
        <p v-if="!changes" class="same muted">No differences.</p>
        <template v-for="(b, bi) in blocks" :key="bi">
            <template v-if="b.type === 'rows'">
                <template v-for="(r, ri) in b.rows" :key="ri">
                    <div class="side" :class="r.kind">
                        <span class="ln">{{ r.l?.n ?? '' }}</span><span class="code mono">{{ r.l?.t ?? '' }}</span>
                    </div>
                    <div class="side r" :class="r.kind">
                        <span class="ln">{{ r.r?.n ?? '' }}</span><span class="code mono">{{ r.r?.t ?? '' }}</span>
                    </div>
                </template>
            </template>
            <div v-else class="skip muted">{{ b.count }} unchanged line{{ b.count === 1 ? '' : 's' }}</div>
        </template>
    </div>
</template>

<style scoped>
.diff {
    border-radius: 8px;
    box-shadow: none;
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    overflow: auto;
    font-size: 12px;
}
.cap {
    padding: 6px 10px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.045em;
    text-transform: uppercase;
    color: var(--text3);
    border-bottom: 1px solid var(--border);
    position: sticky;
    top: 0;
    background: var(--surface);
    z-index: 1;
}
.cap.r,
.side.r {
    border-left: 1px solid var(--border);
}
.n {
    text-transform: none;
    letter-spacing: 0;
    font-weight: 400;
    margin-left: 6px;
}
.side {
    display: flex;
    min-width: 0;
}
.ln {
    flex: none;
    width: 38px;
    text-align: right;
    padding-right: 8px;
    color: var(--text3);
    font-size: 11px;
    line-height: 20px;
    user-select: none;
}
.code {
    flex: 1;
    min-width: 0;
    white-space: pre-wrap;
    word-break: break-all;
    line-height: 20px;
    padding-right: 6px;
}
.side.del .code,
.side.chg:not(.r) .code {
    background: var(--danger-soft);
}
.side.add.r .code,
.side.chg.r .code {
    background: var(--success-soft);
}
.side.add:not(.r),
.side.del.r {
    background: var(--s2);
}
.skip {
    grid-column: 1 / -1;
    text-align: center;
    padding: 3px;
    background: var(--s2);
    font-size: 11px;
}
.same {
    grid-column: 1 / -1;
    margin: 8px 10px;
}
</style>
