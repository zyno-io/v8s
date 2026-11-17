<script setup lang="ts">
import type { Flow, FlowStreamHandle } from '@v8s/client';

import { useVirtualizer } from '@tanstack/vue-virtual';
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';

import {
    endpointParts,
    endpointText,
    FLOW_VIEW_PAD,
    flowTime,
    formatNewFlows,
    isDropped,
    portProto,
    ROW_H_FLOW,
    ROW_H_FLOW_PHONE,
    verdictTone
} from '@/lib/flows.ts';
import { dropCompensation, followItems, isAtBottom, newLinesSince, shouldPauseFollow, viewRange } from '@/lib/logs.ts';
import { isPhone } from '@/lib/viewport.ts';

/**
 * Virtualized flow list: fixed-height rows (30px, or 72px cards on a phone), one v-for over the visible slice with plain
 * elements. The ring buffer lives in the Data Worker; this asks it for the flows around the viewport (`handle.setView`, at most
 * once per frame) and paints `handle.flowAt(abs)`.
 */
const props = defineProps<{ handle: FlowStreamHandle; follow: boolean }>();
const emit = defineEmits<{ 'update:follow': [boolean] }>();

const scroller = ref<HTMLElement | null>(null);
const viewH = ref(0);
const rowH = computed(() => (isPhone.value ? ROW_H_FLOW_PHONE : ROW_H_FLOW));
const first = computed(() => props.handle.first.value);
const count = computed(() => Math.max(0, props.handle.total.value - props.handle.first.value));

const virt = useVirtualizer(
    computed(() => ({
        count: count.value,
        getScrollElement: () => scroller.value,
        estimateSize: () => rowH.value,
        overscan: 10
    }))
);
const totalSize = computed(() => virt.value.getTotalSize());
watch(rowH, () => {
    virt.value.measure();
    schedule(true);
});

interface Row {
    abs: number;
    top: number;
    flow: Flow | undefined;
}
const rows = computed<Row[]>(() => {
    void props.handle.version.value;
    const f = first.value;
    const items = props.follow ? followItems(count.value, viewH.value, rowH.value, 0, 10) : virt.value.getVirtualItems();
    return items.map(it => ({ abs: f + it.index, top: it.start, flow: props.handle.flowAt(f + it.index) }));
});

// ---- frame loop: at most one setView and one scroll write per animation frame ----
let raf = 0;
let pin = false;
let lastKey = '';
function schedule(forceView = false) {
    if (forceView) lastKey = '';
    if (raf) return;
    raf = requestAnimationFrame(frame);
}
function frame() {
    raf = 0;
    const el = scroller.value;
    if (!el) return;
    if (pin && props.follow) {
        pin = false;
        setTop(el.scrollHeight);
    }
    const visible = Math.ceil(el.clientHeight / rowH.value) + 1;
    let range: { first: number; count: number } | null = null;
    if (props.follow) {
        range = { first: Math.max(0, props.handle.total.value - visible - FLOW_VIEW_PAD), count: visible + 2 * FLOW_VIEW_PAD };
    } else {
        const items = virt.value.getVirtualItems();
        if (items.length) range = viewRange(items[0]!.index, items[items.length - 1]!.index, first.value, FLOW_VIEW_PAD);
    }
    if (!range) return;
    const key = `${range.first}:${range.count}:${props.follow}`;
    if (key === lastKey) return;
    lastKey = key;
    props.handle.setView(range.first, range.count, props.follow);
}

let lastTop = 0;
let expect = -1;
let expectAt = 0;
function setTop(top: number) {
    const el = scroller.value;
    if (!el) return;
    el.scrollTop = top;
    expect = el.scrollTop;
    expectAt = performance.now();
    lastTop = expect;
}
function onScroll() {
    const el = scroller.value;
    if (!el) return;
    const top = el.scrollTop;
    const mine = performance.now() - expectAt < 150 && Math.abs(top - expect) < 1;
    if (!mine) {
        if (props.follow && shouldPauseFollow(lastTop, top, el.clientHeight, el.scrollHeight)) emit('update:follow', false);
        else if (!props.follow && top > lastTop && isAtBottom(top, el.clientHeight, el.scrollHeight)) {
            emit('update:follow', true);
            pin = true;
        }
    }
    lastTop = top;
    schedule();
}
function resume() {
    pin = true;
    emit('update:follow', true);
    schedule(true);
}
function onKey(e: KeyboardEvent) {
    if (e.key === 'End' && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault();
        resume();
    }
}

watch(
    [() => props.handle.version.value, () => props.handle.total.value],
    () => {
        if (props.follow) pin = true;
        schedule();
    },
    { flush: 'post' }
);
const pausedAt = ref(props.handle.total.value);
watch(
    () => props.follow,
    f => {
        if (f) pin = true;
        else pausedAt.value = props.handle.total.value;
        schedule(true);
    },
    { flush: 'post' }
);
let prevFirst = props.handle.first.value;
watch(
    first,
    f => {
        const delta = dropCompensation(prevFirst, f, rowH.value);
        const el = scroller.value;
        if (el && delta && !props.follow) setTop(Math.max(0, el.scrollTop + delta));
        prevFirst = f;
    },
    { flush: 'post' }
);
// A cleared stream restarts at a higher `first`; a new handle starts clean.
watch(
    () => props.handle,
    h => {
        prevFirst = h.first.value;
        pausedAt.value = h.total.value;
        h.setPaused(document.hidden);
        if (props.follow) pin = true;
        schedule(true);
    }
);
const newFlows = computed(() => (props.follow ? 0 : newLinesSince(pausedAt.value, props.handle.total.value)));

function onVisibility() {
    props.handle.setPaused(document.hidden);
}
let ro: ResizeObserver | null = null;
onMounted(() => {
    document.addEventListener('visibilitychange', onVisibility);
    props.handle.setPaused(document.hidden);
    viewH.value = scroller.value?.clientHeight ?? 0;
    if (typeof ResizeObserver === 'function' && scroller.value) {
        ro = new ResizeObserver(() => {
            viewH.value = scroller.value?.clientHeight ?? 0;
            schedule(true);
        });
        ro.observe(scroller.value);
    }
    if (props.follow) pin = true;
    schedule(true);
});
onBeforeUnmount(() => {
    document.removeEventListener('visibilitychange', onVisibility);
    ro?.disconnect();
    if (raf) cancelAnimationFrame(raf);
});

const rowTitle = (f: Flow) =>
    `${endpointText(f.src)} to ${endpointText(f.dst)}${f.direction ? ', ' + f.direction.toLowerCase() : ''}${f.node ? ', node ' + f.node : ''}`;
</script>

<template>
    <div class="ft">
        <div v-if="!isPhone" class="head" aria-hidden="true">
            <span class="c-time">Time</span>
            <span class="c-ep">Source</span>
            <span class="c-ep">Destination</span>
            <span class="c-pp">Port</span>
            <span class="c-d">L7 / reason</span>
            <span class="c-v">Verdict</span>
        </div>
        <div
            ref="scroller"
            class="scroller"
            tabindex="0"
            role="log"
            aria-live="off"
            aria-label="Network flows"
            data-testid="flows-viewer"
            @scroll.passive="onScroll"
            @keydown="onKey"
        >
            <div class="sizer" :style="{ height: totalSize + 'px' }">
                <div
                    v-for="r in rows"
                    :key="r.abs"
                    class="frow"
                    :class="{ drop: r.flow && isDropped(r.flow.verdict), card: isPhone }"
                    :style="{ transform: `translateY(${r.top}px)`, height: rowH + 'px' }"
                    :title="r.flow ? rowTitle(r.flow) : undefined"
                >
                    <template v-if="r.flow && !isPhone">
                        <span class="c-time mono">{{ flowTime(r.flow.time) }}</span>
                        <span class="c-ep"
                            >{{ endpointParts(r.flow.src).name
                            }}<span v-if="endpointParts(r.flow.src).ns" class="ns">{{ endpointParts(r.flow.src).ns }}</span
                            ><span v-if="endpointParts(r.flow.src).port" class="ns pt">:{{ endpointParts(r.flow.src).port }}</span></span
                        >
                        <span class="c-ep"
                            >{{ endpointParts(r.flow.dst).name
                            }}<span v-if="endpointParts(r.flow.dst).ns" class="ns">{{ endpointParts(r.flow.dst).ns }}</span
                            ><span class="ns pp-in">{{ portProto(r.flow) }}</span></span
                        >
                        <span class="c-pp mono">{{ portProto(r.flow) }}</span>
                        <span class="c-d mono">{{ r.flow.l7 }}<span v-if="r.flow.dropReason" class="rsn">{{ r.flow.dropReason }}</span></span>
                        <span class="c-v"><span class="tag" :class="verdictTone(r.flow.verdict)">{{ r.flow.verdict }}</span></span>
                    </template>
                    <template v-else-if="r.flow">
                        <span class="l1"
                            ><span class="mono muted">{{ flowTime(r.flow.time) }}</span>
                            <span class="tag" :class="verdictTone(r.flow.verdict)">{{ r.flow.verdict }}</span>
                            <span class="mono muted">{{ portProto(r.flow) }}</span></span
                        >
                        <span class="l2">{{ endpointText(r.flow.src) }} &rarr; {{ endpointText(r.flow.dst) }}</span>
                        <span class="l3 mono muted">{{ r.flow.l7 || r.flow.dropReason }}</span>
                    </template>
                </div>
            </div>
        </div>
        <button v-if="newFlows > 0" type="button" class="pill btn sm primary" data-testid="flows-new-pill" @click="resume">
            {{ formatNewFlows(newFlows) }} <span class="kbd">End</span>
        </button>
    </div>
</template>

<style scoped>
.ft {
    container-type: inline-size;
    position: relative;
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    overflow: hidden;
}
.head,
.frow:not(.card) {
    display: grid;
    grid-template-columns: 92px minmax(90px, 1fr) minmax(90px, 1fr) 76px minmax(110px, 1.4fr) 92px;
    align-items: center;
    column-gap: 10px;
    padding: 0 10px;
}
.head {
    height: 26px;
    flex: none;
    font-size: 11px;
    font-weight: 600;
    color: var(--text3);
    background: var(--s2);
    border-bottom: 1px solid var(--border);
}
.scroller {
    flex: 1;
    min-height: 0;
    overflow: auto;
    contain: strict;
    font-size: 12px;
}
.scroller:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.sizer {
    position: relative;
    width: 100%;
}
.frow {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    contain: layout paint;
    border-bottom: 1px solid var(--border);
    box-sizing: border-box;
}
.frow.drop {
    background: var(--danger-soft);
}
.frow > span,
.c-time,
.c-ep,
.c-pp,
.c-d {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
}
.ns {
    color: var(--text3);
    margin-left: 6px;
}
.ns.pt {
    margin-left: 0;
}
.pp-in {
    display: none;
}
.rsn {
    margin-left: 6px;
    color: var(--danger);
}
.c-time,
.c-pp {
    color: var(--text2);
}
.frow.card {
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 2px;
    padding: 4px 12px;
}
.frow.card > span {
    display: block;
}
.l1 {
    display: flex !important;
    align-items: center;
    gap: 8px;
}
.l2 {
    font-weight: 500;
}
@container (max-width: 640px) {
    .head,
    .frow:not(.card) {
        grid-template-columns: 98px minmax(70px, 1fr) minmax(70px, 1fr) minmax(70px, 1.2fr) 88px;
    }
    .c-pp {
        display: none;
    }
    .pp-in {
        display: inline;
    }
}
.pill {
    position: absolute;
    left: 50%;
    bottom: 14px;
    transform: translateX(-50%);
    box-shadow: var(--float);
}
</style>
