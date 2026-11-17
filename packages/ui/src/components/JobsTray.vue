<script setup lang="ts">
import type { JobFrame } from '@v8s/client';

import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import JobProgress from '@/components/JobProgress.vue';
import { getAgent } from '@/lib/agent.ts';
import { ask } from '@/lib/ask.ts';
import { cancelJob, CANCEL_CONFIRM, drainRequest, groupJobs, isActive, jobDuration, jobLabel, jobTone, progressSummary } from '@/lib/jobs.ts';
import { errorText } from '@/lib/mutations.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useUiStore } from '@/stores/ui.ts';

const agent = getAgent();
const clusters = useClustersStore();
const ui = useUiStore();

const open = ref(false);
const expandedId = ref('');
const now = ref(Date.now());
const announce = ref('');
const btn = ref<HTMLButtonElement | null>(null);
const panel = ref<HTMLElement | null>(null);

const all = shallowRef<readonly JobFrame[]>(agent.jobs.value);
const shown = computed(() => groupJobs(all.value, now.value));
const running = computed(() => all.value.filter(isActive).length);

// Finish notifications: only for jobs this session saw running (a reload's already-finished jobs stay quiet), once per job.
const seenRunning = new Set<string>();
const notified = new Set<string>();
watch(
    () => agent.jobs.value,
    list => {
        all.value = list;
        for (const j of list) {
            if (isActive(j)) {
                seenRunning.add(j.jobId);
                continue;
            }
            if (!seenRunning.has(j.jobId) || notified.has(j.jobId)) continue;
            notified.add(j.jobId);
            const label = jobLabel(j);
            if (j.state === 'done') {
                announce.value = `${label} finished`;
                ui.toast('success', `${label} finished`);
            } else if (j.state === 'failed') {
                announce.value = `${label} failed`;
                ui.toast('danger', `${label} failed${j.error ? `: ${j.error.message}` : ''}`);
            } else {
                announce.value = `${label} ${j.state}`;
            }
        }
    },
    { immediate: true }
);

let tick: ReturnType<typeof setInterval> | undefined;
watch(
    [open, running],
    ([o, r]) => {
        clearInterval(tick);
        tick = undefined;
        if (o && r) tick = setInterval(() => (now.value = Date.now()), 1000);
        else now.value = Date.now();
    },
    { immediate: true }
);
onBeforeUnmount(() => {
    clearInterval(tick);
    document.removeEventListener('pointerdown', onOutside, true);
});

function onOutside(e: Event) {
    const t = e.target as Node;
    if (panel.value?.contains(t) || btn.value?.contains(t)) return;
    close(false);
}
async function toggle() {
    if (open.value) return close(true);
    open.value = true;
    document.addEventListener('pointerdown', onOutside, true);
    await nextTick();
    panel.value?.focus();
}
function close(refocus: boolean) {
    open.value = false;
    document.removeEventListener('pointerdown', onOutside, true);
    if (refocus) btn.value?.focus();
}

async function cancel(j: JobFrame) {
    if (!(await ask({ title: `Cancel ${jobLabel(j)}?`, body: CANCEL_CONFIRM, confirmLabel: 'Cancel job', danger: true }))) return;
    try {
        await cancelJob(j.jobId);
    } catch (e) {
        ui.toast('danger', errorText(e));
    }
}
function reopen(j: JobFrame) {
    if (!j.cluster || !j.subject) return;
    drainRequest.value = { cluster: j.cluster, node: j.subject, jobId: j.jobId };
    close(false);
}
const started = (ms: number) => new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
</script>

<template>
    <div v-if="all.length" class="jobs">
        <button
            ref="btn"
            class="btn ghost jbtn"
            type="button"
            data-testid="jobs-tray-btn"
            aria-haspopup="dialog"
            :aria-expanded="open"
            :aria-label="running ? `Jobs, ${running} running` : 'Jobs'"
            @click="toggle"
        >
            Jobs<span v-if="running" class="tag warning badge" aria-hidden="true">{{ running }}</span>
        </button>
        <div
            v-if="open"
            ref="panel"
            class="panel floating"
            role="dialog"
            aria-label="Jobs"
            tabindex="-1"
            data-testid="jobs-panel"
            @keydown.esc.stop="close(true)"
        >
            <p v-if="!shown.length" class="muted empty">No recent jobs.</p>
            <ul class="list">
                <li v-for="j in shown" :key="j.jobId" data-testid="job-row" class="row">
                    <div class="top">
                        <button
                            class="exp"
                            type="button"
                            :aria-expanded="expandedId === j.jobId"
                            :aria-label="`${expandedId === j.jobId ? 'Collapse' : 'Expand'} ${jobLabel(j)}`"
                            @click="expandedId = expandedId === j.jobId ? '' : j.jobId"
                        >
                            <span aria-hidden="true">{{ expandedId === j.jobId ? '▾' : '▸' }}</span>
                            <b class="label">{{ jobLabel(j) }}</b>
                        </button>
                        <span v-if="j.cluster" class="tag">{{ clusters.nameById.get(j.cluster) ?? j.cluster }}</span>
                        <span class="tag" :class="jobTone(j.state)">{{ j.state }}</span>
                    </div>
                    <div class="meta muted">
                        <span>{{ started(j.startedAt) }} · {{ jobDuration(j, now) }}</span>
                        <span>{{ progressSummary(j) }}</span>
                    </div>
                    <div v-if="isActive(j) || (j.type === 'drain' && j.cluster && j.subject)" class="acts">
                        <button v-if="isActive(j)" class="btn sm danger" type="button" @click="cancel(j)">Cancel</button>
                        <button v-if="j.type === 'drain' && j.cluster && j.subject" class="btn sm" type="button" @click="reopen(j)">Open</button>
                    </div>
                    <JobProgress v-if="expandedId === j.jobId" :job="j" />
                </li>
            </ul>
        </div>
        <span class="sr" role="status" aria-live="polite">{{ announce }}</span>
    </div>
</template>

<style scoped>
.jobs {
    position: relative;
    display: inline-flex;
}
.badge {
    margin-left: 6px;
}
.panel {
    position: fixed;
    top: 52px;
    right: 12px;
    width: min(460px, calc(100vw - 24px));
    max-height: calc(100vh - 70px);
    overflow: auto;
    z-index: 60;
    padding: 6px;
    border-radius: 10px;
    color: var(--text);
}
.panel:focus {
    outline: none;
}
.list {
    list-style: none;
    margin: 0;
    padding: 0;
}
.row {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px;
    font-size: 13px;
}
.row + .row {
    border-top: 1px solid var(--neutral-b);
}
.top {
    display: flex;
    gap: 6px;
    align-items: center;
}
.exp {
    all: unset;
    box-sizing: border-box;
    flex: 1;
    display: flex;
    gap: 6px;
    align-items: center;
    cursor: pointer;
    min-height: 28px;
    min-width: 0;
}
.exp:focus-visible {
    outline: 2px solid var(--accent, currentColor);
    outline-offset: 2px;
}
.label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.meta {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    font-size: 12px;
}
.acts {
    display: flex;
    gap: 6px;
}
.empty {
    padding: 10px;
    margin: 0;
}
.sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
}
@media (max-width: 639.98px) {
    .panel {
        left: 0;
        right: 0;
        width: auto;
        border-radius: 0 0 10px 10px;
    }
}
@media (pointer: coarse) {
    .exp,
    .acts .btn {
        min-height: 44px;
    }
}
</style>
