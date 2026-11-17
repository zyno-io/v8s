<script setup lang="ts">
import type { LogLineView, LogStreamHandle } from '@v8s/client';

import { useVirtualizer } from '@tanstack/vue-virtual';
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';

import {
    centerScrollTop,
    formatNewLines,
    MARKER_H,
    newLinesSince,
    OVERSCAN,
    rowHeight,
    runStyle,
    shortTimestamp,
    shouldPauseFollow,
    isAtBottom,
    splitRuns,
    sourceVar,
    viewRange,
    VIEW_PAD,
    dropCompensation,
    followItems,
    type Run
} from '@/lib/logs.ts';

/**
 * Virtualized log list. Rows are a fixed height (18px; wrap mode is a fixed 2-line 36px row with the text clamped, a deliberate
 * limitation so the virtualizer never measures). The ring buffer, ANSI parsing, level detection and search live in the Data
 * Worker; this component only asks it for the lines around the viewport (`handle.setView`, at most once per animation frame)
 * and paints `handle.lineAt(abs)` for the visible slice.
 */
const props = withDefaults(defineProps<{ handle: LogStreamHandle; follow: boolean; timestamps: boolean; wrap: boolean; showNumbers?: boolean }>(), {
    showNumbers: true
});
const emit = defineEmits<{ 'update:follow': [boolean]; download: [] }>();

const scroller = ref<HTMLElement | null>(null);
const viewH = ref(0);
const rowH = computed(() => rowHeight(props.wrap));
const first = computed(() => props.handle.first.value);
const count = computed(() => Math.max(0, props.handle.total.value - props.handle.first.value));
const padStart = computed(() => (first.value > 0 ? MARKER_H : 0));

const virt = useVirtualizer(
    computed(() => ({
        count: count.value,
        getScrollElement: () => scroller.value,
        estimateSize: () => rowH.value,
        overscan: OVERSCAN,
        paddingStart: padStart.value
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
    line: LogLineView | undefined;
    runs: Run[] | null;
    level: 2 | 3 | 0;
    cur: boolean;
}

const rows = computed<Row[]>(() => {
    void props.handle.version.value;
    const f = first.value;
    const cur = props.handle.reveal.value?.line ?? -1;
    const items = props.follow ? followItems(count.value, viewH.value, rowH.value, padStart.value) : virt.value.getVirtualItems();
    return items.map(it => {
        const abs = f + it.index;
        const line = props.handle.lineAt(abs);
        // Plain lines (the vast majority) render one text node; only styled / highlighted lines split into runs.
        const runs = line && (line.spans?.length || line.hl?.length) ? splitRuns(line.text, line.spans, line.hl) : null;
        return { abs, top: it.start, line, runs, level: line?.level ?? 0, cur: abs === cur };
    });
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
        range = { first: Math.max(0, props.handle.total.value - visible - VIEW_PAD), count: visible + 2 * VIEW_PAD };
    } else {
        const items = virt.value.getVirtualItems();
        if (items.length) range = viewRange(items[0]!.index, items[items.length - 1]!.index, first.value);
    }
    if (!range) return;
    const key = `${range.first}:${range.count}:${props.follow}`;
    if (key === lastKey) return;
    lastKey = key;
    props.handle.setView(range.first, range.count, props.follow);
}

// ---- scrolling ----

let lastTop = 0;
let expect = -1;
let expectAt = 0;
/** Programmatic scroll write; the resulting scroll event must not be mistaken for the user. */
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

// New data: pin to the bottom while following (one write per frame via the frame loop).
watch(
    [() => props.handle.version.value, () => props.handle.total.value],
    () => {
        if (props.follow) pin = true;
        schedule();
    },
    { flush: 'post' }
);

watch(
    () => props.follow,
    f => {
        if (f) pin = true;
        else pausedAt.value = props.handle.total.value;
        schedule(true);
    },
    { flush: 'post' }
);

// Lines dropped from the front while paused: shift scrollTop so the visible lines stay put.
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

// Search next/prev: centre the line and pause follow.
watch(
    () => props.handle.reveal.value,
    r => {
        const el = scroller.value;
        if (!r || !el) return;
        const idx = r.line - first.value;
        if (idx < 0) return;
        if (props.follow) emit('update:follow', false);
        setTop(centerScrollTop(idx, rowH.value, el.clientHeight, padStart.value));
        schedule(true);
    },
    { flush: 'post' }
);

// New handle (stream re-opened): follow state is the parent's; start from a clean view.
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

// ---- "N new lines" pill ----

const pausedAt = ref(props.handle.total.value);
const newLines = computed(() => (props.follow ? 0 : newLinesSince(pausedAt.value, props.handle.total.value)));

// ---- lifecycle ----

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
    props.handle.setPaused(false);
});

const levelWord = (l: 2 | 3 | 0) => (l === 3 ? 'error' : l === 2 ? 'warning' : '');
</script>

<template>
    <div class="lv">
        <div
            ref="scroller"
            class="scroller"
            :class="{ wrap }"
            tabindex="0"
            role="log"
            aria-live="off"
            aria-label="Log output"
            data-testid="logs-viewer"
            @scroll.passive="onScroll"
            @keydown="onKey"
        >
            <div class="sizer" :style="{ height: totalSize + 'px' }">
                <div v-if="first > 0" class="marker" :style="{ height: MARKER_H + 'px' }">
                    <span>{{ first.toLocaleString('en-US') }} earlier lines dropped,</span>
                    <button type="button" class="link" @click="emit('download')">download full log</button>
                </div>
                <div
                    v-for="r in rows"
                    :key="r.abs"
                    class="lrow"
                    :class="{ cur: r.cur }"
                    :style="{ transform: `translateY(${r.top}px)`, height: rowH + 'px' }"
                >
                    <template v-if="r.line">
                        <span class="gut" :class="{ err: r.level === 3, warn: r.level === 2 }" role="presentation"></span>
                        <span v-if="r.level" class="sr-only">{{ levelWord(r.level) }}</span>
                        <span v-if="showNumbers" class="num">{{ r.abs + 1 }}</span>
                        <span v-if="r.line.src" class="src" :style="{ color: sourceVar(r.line.src) }">{{ r.line.src }}</span>
                        <span v-if="timestamps && r.line.ts" class="ts">{{ shortTimestamp(r.line.ts) }}</span>
                        <span v-if="!r.runs" class="txt">{{ r.line.text }}</span>
                        <span v-else class="txt"
                            ><template v-for="(run, i) in r.runs" :key="i"
                                ><mark v-if="run.hl" :style="runStyle(run)">{{ run.text }}</mark
                                ><span v-else :style="runStyle(run)">{{ run.text }}</span></template
                            ></span
                        >
                    </template>
                </div>
            </div>
        </div>
        <button v-if="newLines > 0" type="button" class="pill btn sm primary" data-testid="logs-new-pill" @click="resume">
            {{ formatNewLines(newLines) }} <span class="kbd">End</span>
        </button>
    </div>
</template>

<style lang="scss">
@mixin ansi-light {
    --ansi-0: #15171c;
    --ansi-1: #c4320a;
    --ansi-2: #067647;
    --ansi-3: #a15c07;
    --ansi-4: #1e5bd6;
    --ansi-5: #6927da;
    --ansi-6: #0e7490;
    --ansi-7: #646a78;
    --ansi-8: #4f5562;
    --ansi-9: #b42318;
    --ansi-10: #0a7a4a;
    --ansi-11: #8a5100;
    --ansi-12: #3d37c9;
    --ansi-13: #7a2fe0;
    --ansi-14: #0b6a8a;
    --ansi-15: #3a3f4b;
}
@mixin ansi-dark {
    --ansi-0: #5c6270;
    --ansi-1: #ff8a73;
    --ansi-2: #5ad49a;
    --ansi-3: #f8c55c;
    --ansi-4: #8db4ff;
    --ansi-5: #c2a8ff;
    --ansi-6: #67d4e8;
    --ansi-7: #aeb2bd;
    --ansi-8: #8b909c;
    --ansi-9: #ff9c88;
    --ansi-10: #7ee8b4;
    --ansi-11: #ffd98a;
    --ansi-12: #a7c4ff;
    --ansi-13: #d6c2ff;
    --ansi-14: #8fe3f2;
    --ansi-15: #ececf0;
}
// Pod label colours for merged streams, taken from the status tokens (no danger tone: that is reserved for error gutters).
.lv {
    @include ansi-light;
    --log-src-0: var(--info);
    --log-src-1: var(--success);
    --log-src-2: var(--release);
    --log-src-3: var(--warning);
    --log-src-4: var(--link);
    --log-src-5: var(--neutral);
    --log-src-6: var(--focus);
    --log-src-7: var(--text2);
}
@media (prefers-color-scheme: dark) {
    :root:not(.light) .lv {
        @include ansi-dark;
    }
}
:root.dark .lv {
    @include ansi-dark;
}
</style>

<style scoped>
.lv {
    position: relative;
    flex: 1;
    min-height: 0;
    display: flex;
}
.scroller {
    flex: 1;
    min-width: 0;
    overflow: auto;
    contain: strict;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    font-family: ui-monospace, Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 18px;
    color: var(--text);
    user-select: text;
    -webkit-user-select: text;
    cursor: text;
}
.scroller:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.sizer {
    position: relative;
    min-width: 100%;
    width: max-content;
}
.wrap .sizer {
    width: 100%;
}
.marker {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 0 10px;
    background: var(--warning-soft);
    border-bottom: 1px solid var(--warning-b);
    color: var(--text2);
    font-family: inherit;
}
.link {
    border: 0;
    background: none;
    padding: 0;
    color: var(--link);
    font: inherit;
    text-decoration: underline;
    cursor: pointer;
}
.lrow {
    position: absolute;
    top: 0;
    left: 0;
    min-width: 100%;
    display: flex;
    align-items: flex-start;
    gap: 8px;
    contain: layout paint;
    white-space: pre;
}
.wrap .lrow {
    width: 100%;
}
.lrow.cur {
    box-shadow: inset 0 0 0 2px var(--focus);
}
.gut {
    flex: none;
    width: 4px;
    align-self: stretch;
}
.gut.err {
    background: var(--danger);
}
.gut.warn {
    background: var(--warning);
}
.num {
    flex: none;
    min-width: 6ch;
    text-align: right;
    color: var(--text3);
    user-select: none;
    -webkit-user-select: none;
}
.src {
    flex: none;
    font-weight: 600;
}
.ts {
    flex: none;
    color: var(--text3);
}
.txt {
    flex: 1;
    min-width: 0;
}
.wrap .txt {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    overflow: hidden;
}
mark {
    background: var(--warning-b);
    color: inherit;
    border-radius: 2px;
}
.pill {
    position: absolute;
    left: 50%;
    bottom: 14px;
    transform: translateX(-50%);
    box-shadow: var(--float);
}
</style>
