<script setup lang="ts">
import type { JobFrame } from '@v8s/client';

import { computed } from 'vue';

import { isActive, itemLabel, itemRows, itemTone, jobDuration, jobLabel, jobTone, progressSummary } from '@/lib/jobs.ts';

const props = defineProps<{ job: JobFrame }>();

const parts = computed(() => itemRows(props.job));
const summary = computed(() => progressSummary(props.job));
const active = computed(() => isActive(props.job));
const final = computed(() => {
    const j = props.job;
    if (isActive(j)) return '';
    const what = jobLabel(j);
    const d = jobDuration(j);
    if (j.state === 'done') return `${what} finished in ${d}. ${summary.value}`.trim();
    if (j.state === 'cancelled') return `${what} was cancelled after ${d}. ${summary.value}`.trim();
    return `${what} failed after ${d}. ${summary.value}`.trim();
});
const time = (ms: number) => new Date(ms).toLocaleTimeString();
</script>

<template>
    <div class="jp" data-testid="job-progress">
        <div class="head">
            <span class="tag" :class="jobTone(job.state)">{{ job.state }}</span>
            <span class="sum" aria-live="polite">{{ summary }}</span>
        </div>
        <div v-if="job.error" class="err" role="alert">
            <b>{{ job.error.message }}</b>
            <span v-if="job.error.hint" class="hint">{{ job.error.hint }}</span>
        </div>
        <ul v-if="parts.rows.length" class="rows" aria-label="Pods">
            <li v-for="r in parts.rows" :key="r.item">
                <span class="mono item">{{ r.item }}</span>
                <span class="tag" :class="itemTone(r.state)">{{ itemLabel(r.state) }}</span>
                <span v-if="r.message" class="msg muted">{{ r.message }}</span>
            </li>
        </ul>
        <ul v-if="parts.log.length" class="log" aria-label="Log">
            <li v-for="(l, i) in parts.log" :key="i"><span class="mono muted">{{ time(l.at) }}</span> {{ l.message }}</li>
        </ul>
        <p v-if="job.progressDropped" class="muted small">{{ job.progressDropped }} older entries not shown.</p>
        <p v-if="active" class="muted small">Running for {{ jobDuration(job) }}. Closing this view does not stop the job.</p>
        <p v-else class="final" :class="job.state">{{ final }}</p>
    </div>
</template>

<style scoped>
.jp {
    display: flex;
    flex-direction: column;
    gap: 8px;
    font-size: 13px;
}
.head {
    display: flex;
    gap: 8px;
    align-items: center;
}
.sum {
    color: var(--text2);
}
ul {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 260px;
    overflow: auto;
    border: 1px solid var(--border, var(--neutral-b));
    border-radius: 8px;
}
li {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 5px 10px;
    min-height: 30px;
}
li + li {
    border-top: 1px solid var(--border, var(--neutral-b));
}
.item {
    flex: 1 1 200px;
    word-break: break-all;
}
.msg {
    flex: 1 1 100%;
    font-size: 12px;
}
.log li {
    display: block;
    font-size: 12px;
}
.err {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 8px 10px;
    border-radius: 8px;
    border: 1px solid var(--danger-b);
    background: var(--danger-soft);
    color: var(--danger);
}
.hint {
    color: var(--text2);
}
p {
    margin: 0;
}
.small {
    font-size: 12px;
}
.final {
    font-weight: 500;
}
.final.failed {
    color: var(--danger);
}
.final.done {
    color: var(--success);
}
</style>
