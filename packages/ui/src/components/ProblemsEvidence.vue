<script setup lang="ts">
import { computed } from 'vue';
import { useRouter } from 'vue-router';

import type { EvState, ProblemAction, ProblemRow, SnoozePreset } from '@/lib/problems.ts';

import { kinds } from '@/lib/agent.ts';
import { openObject } from '@/lib/drawer.ts';
import { formatAge } from '@/lib/format.ts';
import { canRun, relatedRef, runProblemAction } from '@/lib/problems-actions.ts';
import { ACTION_LABEL, actionsForClass, severityTone, SNOOZE_PRESETS } from '@/lib/problems.ts';
import { useClustersStore } from '@/stores/clusters.ts';

const clusters = useClustersStore();
const props = defineProps<{ row: ProblemRow; state: EvState | null; phone?: boolean }>();
const emit = defineEmits<{ ack: [preset: SnoozePreset | null]; unack: [] }>();

const router = useRouter();
const now = Date.now();
const ev = computed(() => (props.state?.status === 'ok' ? (props.state.data ?? null) : null));
const ctx = computed(() => ({ router, kinds: kinds.value, row: props.row, ev: ev.value }));
const actions = computed<ProblemAction[]>(() =>
    actionsForClass(props.row.class, ev.value?.subject.kind || props.row.kind).filter(a => canRun(a, ctx.value))
);

function run(a: ProblemAction) {
    void runProblemAction(a, ctx.value);
}
function open(r: { gvr?: string; kind: string; namespace?: string; name: string }) {
    const ref = relatedRef(props.row.cluster, r, kinds.value);
    if (ref) void openObject(router, ref);
}
const openable = (r: { gvr?: string; kind: string }) => !!(r.gvr || kinds.value.some(k => k.kind === r.kind));
const age = (t?: number) => (t ? formatAge(t, now) : '—');
</script>

<template>
    <section class="ev" :class="{ phone }" aria-label="Problem evidence" data-testid="problem-evidence">
        <header class="evh">
            <span class="tag" :class="severityTone(row.severity)">{{ row.severity }}</span>
            <span class="tag" :class="clusters.tagTone(row.cluster)"
                ><span class="dot" :style="{ background: clusters.dotColor(row.cluster) }"></span>{{ clusters.displayName(row.cluster) }}</span
            >
            <span class="mono muted">{{ row.class }}</span>
            <b class="ttl">{{ ev?.title || row.title }}</b>
            <span v-if="row.message" class="muted msg">{{ row.message }}</span>
        </header>
        <div class="bar" role="toolbar" aria-label="Quick actions">
            <button v-for="a in actions" :key="a" class="btn sm" type="button" :data-testid="`pa-${a}`" @click="run(a)">{{ ACTION_LABEL[a] }}</button>
            <span class="grow"></span>
            <template v-if="!row.acked">
                <button class="btn sm" type="button" data-testid="pa-ack" @click="emit('ack', null)">Acknowledge</button>
                <button v-for="p in SNOOZE_PRESETS" :key="p.id" class="btn sm ghost" type="button" @click="emit('ack', p.id)">
                    {{ p.label.replace('Snooze ', '') }}
                </button>
            </template>
            <button v-else class="btn sm" type="button" data-testid="pa-restore" @click="emit('unack')">Restore</button>
        </div>
        <p v-if="!state || state.status === 'loading'" class="muted pad" role="status">Loading evidence…</p>
        <p v-else-if="state.status === 'error'" class="notice warn pad" role="alert">{{ state.error }}</p>
        <div v-else-if="ev" class="body">
            <p v-if="ev.error" class="notice warn" role="status">The live object could not be read ({{ ev.error }}); showing cached evidence.</p>
            <div v-if="ev.conditions.length" class="sec">
                <h4>Conditions</h4>
                <div v-for="(c, i) in ev.conditions" :key="i" class="li">
                    <span class="mono">{{ c.type }}={{ c.status }}</span>
                    <span v-if="c.reason" class="tag">{{ c.reason }}</span>
                    <span class="muted t">{{ c.message }}</span>
                    <span class="muted">{{ age(c.since) }}</span>
                </div>
            </div>
            <div v-if="ev.containers.length" class="sec">
                <h4>Containers</h4>
                <div v-for="(c, i) in ev.containers" :key="i" class="li">
                    <b>{{ c.name }}</b>
                    <span class="tag" :class="c.state === 'running' ? 'success' : c.state === 'waiting' ? 'warning' : 'danger'">{{
                        c.reason || c.state
                    }}</span>
                    <span v-if="c.exitCode != null && c.state === 'terminated'" class="mono">exit {{ c.exitCode }}</span>
                    <span class="mono" :class="{ bad: c.restarts > 5 }">restarts {{ c.restarts }}</span>
                    <span v-if="c.lastReason" class="muted">last: {{ c.lastReason }}</span>
                    <span v-if="c.pod" class="mono muted">{{ c.pod }}</span>
                    <span class="muted">{{ age(c.since) }}</span>
                    <span v-if="c.message" class="muted t">{{ c.message }}</span>
                </div>
            </div>
            <div v-if="ev.events.length" class="sec">
                <h4>Warning events</h4>
                <div v-for="(e, i) in ev.events" :key="i" class="li">
                    <span class="tag warning">{{ e.reason }}</span>
                    <span v-if="e.count > 1" class="mono">×{{ e.count }}</span>
                    <span v-if="e.object" class="mono muted">{{ e.object }}</span>
                    <span class="t">{{ e.message }}</span>
                    <span class="muted">{{ age(e.lastSeen) }}</span>
                </div>
            </div>
            <div v-if="ev.related.length" class="sec">
                <h4>Related</h4>
                <div v-for="(r, i) in ev.related" :key="i" class="li">
                    <span class="muted">{{ r.kind }}</span>
                    <button v-if="openable(r)" class="lnk" type="button" @click="open(r)">
                        {{ r.namespace ? r.namespace + '/' : '' }}{{ r.name }}
                    </button>
                    <b v-else>{{ r.name }}</b>
                    <span v-if="r.note" class="muted t">{{ r.note }}</span>
                </div>
            </div>
            <p v-if="!ev.conditions.length && !ev.containers.length && !ev.events.length && !ev.related.length" class="muted">No further evidence.</p>
        </div>
    </section>
</template>

<style scoped>
.ev {
    height: 100%;
    overflow: auto;
    padding: 8px 16px 10px 44px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    background: var(--bg);
    font-size: 12px;
}
.ev.phone {
    height: auto;
    padding: 12px 14px 24px;
    background: var(--surface);
    font-size: 13px;
}
.evh {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.ttl {
    font-size: 13px;
}
.msg {
    flex-basis: 100%;
}
.bar {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    align-items: center;
}
.body {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
    gap: 10px 24px;
    align-items: start;
}
.ev.phone .body {
    display: flex;
    flex-direction: column;
}
.body > .notice {
    grid-column: 1 / -1;
}
.sec h4 {
    margin: 0 0 2px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.045em;
    text-transform: uppercase;
    color: var(--text3);
}
.li {
    display: flex;
    align-items: baseline;
    gap: 8px;
    flex-wrap: wrap;
    padding: 2px 0;
    min-width: 0;
}
.t {
    min-width: 0;
    overflow-wrap: anywhere;
}
.bad {
    color: var(--danger);
}
.lnk {
    all: unset;
    cursor: pointer;
    font-weight: 600;
    color: var(--accent);
}
.lnk:hover {
    text-decoration: underline;
}
.lnk:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 1px;
}
.pad {
    margin: 0;
}
@media (pointer: coarse) {
    .bar .btn {
        height: 44px;
    }
    .lnk {
        padding: 10px 0;
    }
}
</style>
