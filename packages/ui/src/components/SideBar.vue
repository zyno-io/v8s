<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useRoute } from 'vue-router';

import SideNav from '@/components/SideNav.vue';
import { SIDEBAR_DEFAULT, SIDEBAR_MAX, SIDEBAR_MIN, SIDEBAR_STEP, clampSidebarWidth } from '@/lib/sidebar.ts';
import { useSidebarStore } from '@/stores/sidebar.ts';

/** The desktop sidebar frame: pinned in the layout, or collapsed to an edge that opens it as an overlay. Resizable by its right edge. */
const sidebar = useSidebarStore();
const route = useRoute();
const host = ref<HTMLElement | null>(null);
const panel = ref<HTMLElement | null>(null);
const handle = ref<HTMLElement | null>(null);

const OPEN_DELAY = 150;
const CLOSE_DELAY = 300;
let openTimer: ReturnType<typeof setTimeout> | undefined;
let closeTimer: ReturnType<typeof setTimeout> | undefined;
const clearTimers = () => {
    clearTimeout(openTimer);
    clearTimeout(closeTimer);
};

// ---- overlay open / close ----
function edgeEnter(e: PointerEvent) {
    if (e.pointerType === 'touch' || sidebar.pinned || sidebar.open) return;
    clearTimeout(openTimer);
    openTimer = setTimeout(() => (sidebar.open = true), OPEN_DELAY);
}
function edgeLeave() {
    clearTimeout(openTimer);
}
function panelEnter() {
    clearTimeout(closeTimer);
}
function panelLeave(e: PointerEvent) {
    if (sidebar.pinned || dragging || e.pointerType === 'touch') return;
    clearTimeout(closeTimer);
    closeTimer = setTimeout(() => {
        // Someone typing in the panel (bookmark name) keeps it open.
        const a = document.activeElement;
        if (a && panel.value?.contains(a) && a.matches('input, textarea, select')) return;
        sidebar.open = false;
    }, CLOSE_DELAY);
}
function panelClick(e: MouseEvent) {
    if (!sidebar.pinned && e.target instanceof Element && e.target.closest('a')) sidebar.open = false;
}
function outside(e: PointerEvent) {
    if (sidebar.pinned || !sidebar.open) return;
    if (e.target instanceof Element && (host.value?.contains(e.target) || e.target.closest('[data-sidebar-toggle]'))) return;
    sidebar.open = false;
}
onMounted(() => document.addEventListener('pointerdown', outside, true));
onBeforeUnmount(() => {
    cancelDrag?.(true);
    document.removeEventListener('pointerdown', outside, true);
    clearTimers();
});
watch(
    () => route.fullPath,
    () => {
        if (!sidebar.pinned) sidebar.open = false;
    }
);
const toggleEl = () => document.querySelector<HTMLElement>('[data-sidebar-toggle]');
// Sync, so we can see whether focus was inside the panel before it is removed.
watch(
    () => sidebar.open,
    open => {
        if (open) {
            if (sidebar.focusOnOpen) void nextTick(() => panel.value?.querySelector<HTMLElement>('[data-testid=sidebar-pin]')?.focus());
            sidebar.focusOnOpen = false;
            return;
        }
        clearTimers();
        const had = !!panel.value && panel.value.contains(document.activeElement);
        if (had) void nextTick(() => toggleEl()?.focus());
    },
    { flush: 'sync' }
);
watch(
    () => sidebar.pinned || sidebar.open,
    shown => {
        if (!shown) cancelDrag?.(true);
    },
    { flush: 'sync' }
);
function togglePin() {
    const wasPinned = sidebar.pinned;
    sidebar.togglePinned();
    if (wasPinned) void nextTick(() => toggleEl()?.focus());
}

// ---- resize ----
// The width is a CSS variable on the host: a drag writes it straight to the element once per frame (no Vue re-render, no store churn);
// the table beside it just reflows. The final width is committed (and persisted) once, on release.
let dragging = false;
let raf = 0;
let pending = 0;
/** Ends the drag in progress (if any): commit the dragged width, or restore the starting one. */
let cancelDrag: ((commit: boolean) => void) | null = null;
function paint(w: number) {
    pending = w;
    if (raf) return;
    raf = requestAnimationFrame(() => {
        raf = 0;
        host.value?.style.setProperty('--sidebar-w', `${pending}px`);
        handle.value?.setAttribute('aria-valuenow', String(pending));
    });
}
function startDrag(e: PointerEvent) {
    if (e.button !== 0 || dragging) return;
    const el = handle.value;
    if (!el) return;
    e.preventDefault();
    try {
        el.setPointerCapture(e.pointerId);
    } catch {
        // the window listeners below still follow the pointer
    }
    dragging = true;
    clearTimeout(closeTimer);
    const startX = e.clientX;
    const startW = sidebar.width;
    let w = startW;
    const prevCursor = document.body.style.cursor;
    const prevSelect = document.body.style.userSelect;
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
    const move = (ev: PointerEvent) => {
        w = clampSidebarWidth(startW + ev.clientX - startX);
        paint(w);
    };
    const up = () => finish(true);
    const key = (ev: KeyboardEvent) => {
        if (ev.key !== 'Escape') return;
        ev.preventDefault();
        ev.stopPropagation();
        finish(false);
    };
    function finish(commit: boolean) {
        window.removeEventListener('pointermove', move);
        window.removeEventListener('pointerup', up);
        window.removeEventListener('pointercancel', up);
        window.removeEventListener('keydown', key, true);
        cancelDrag = null;
        dragging = false;
        document.body.style.cursor = prevCursor;
        document.body.style.userSelect = prevSelect;
        if (raf) cancelAnimationFrame(raf);
        raf = 0;
        if (commit) {
            if (w !== startW) sidebar.commitWidth(w);
        } else {
            host.value?.style.setProperty('--sidebar-w', `${startW}px`);
            handle.value?.setAttribute('aria-valuenow', String(startW));
        }
    }
    cancelDrag = finish;
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
    window.addEventListener('pointercancel', up);
    window.addEventListener('keydown', key, true);
}
function resizeKey(e: KeyboardEvent) {
    const w = sidebar.width;
    const next =
        e.key === 'ArrowLeft'
            ? w - SIDEBAR_STEP
            : e.key === 'ArrowRight'
              ? w + SIDEBAR_STEP
              : e.key === 'Home'
                ? SIDEBAR_MIN
                : e.key === 'End'
                  ? SIDEBAR_MAX
                  : null;
    if (next === null) return;
    e.preventDefault();
    sidebar.nudgeWidth(next);
}
const resetWidth = () => sidebar.commitWidth(SIDEBAR_DEFAULT);
</script>

<template>
    <div
        ref="host"
        class="sidebar-host"
        :class="{ pinned: sidebar.pinned, collapsed: !sidebar.pinned }"
        :style="{ '--sidebar-w': `${sidebar.width}px` }"
        data-testid="sidebar"
    >
        <div
            v-if="!sidebar.pinned"
            class="edge"
            aria-hidden="true"
            data-testid="sidebar-edge"
            @pointerenter="edgeEnter"
            @pointerleave="edgeLeave"
        ></div>
        <Transition name="slide">
            <div
                v-if="sidebar.pinned || sidebar.open"
                ref="panel"
                class="panel"
                :class="{ overlay: !sidebar.pinned }"
                data-testid="sidebar-panel"
                @pointerenter="panelEnter"
                @pointerleave="panelLeave"
                @click="panelClick"
            >
                <div class="head">
                    <span class="title">Resources</span>
                    <button
                        class="pin"
                        :class="{ on: sidebar.pinned }"
                        type="button"
                        :aria-pressed="sidebar.pinned"
                        aria-label="Pin sidebar"
                        title="Pin sidebar ([)"
                        data-testid="sidebar-pin"
                        @click="togglePin()"
                    >
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
                            <rect x="2" y="3" width="12" height="10" rx="2" />
                            <path d="M6 3v10" />
                            <path v-if="sidebar.pinned" d="M9 6.5L7.7 8 9 9.5" />
                            <path v-else d="M10 6.5L11.3 8 10 9.5" />
                        </svg>
                    </button>
                </div>
                <SideNav />
                <div
                    ref="handle"
                    class="resizer"
                    role="separator"
                    aria-orientation="vertical"
                    aria-label="Resize sidebar"
                    tabindex="0"
                    :aria-valuenow="sidebar.width"
                    :aria-valuemin="SIDEBAR_MIN"
                    :aria-valuemax="SIDEBAR_MAX"
                    data-testid="sidebar-resizer"
                    @pointerdown="startDrag"
                    @keydown="resizeKey"
                    @dblclick="resetWidth"
                ></div>
            </div>
        </Transition>
    </div>
</template>

<style scoped>
.sidebar-host {
    position: relative;
    flex: none;
}
/* Pinned it sits beside the content (z 1 only so the resizer overhang is grabbable); collapsed, the overlay floats above it. */
.sidebar-host.pinned {
    width: var(--sidebar-w);
    z-index: 1;
}
.sidebar-host.collapsed {
    width: 0;
    z-index: 30;
}
.panel > :deep(.side) {
    flex: 1 1 0;
    min-height: 0;
}
.edge {
    position: absolute;
    inset: 0 auto 0 0;
    width: 8px;
}
.panel {
    position: relative;
    height: 100%;
    width: var(--sidebar-w);
    display: flex;
    flex-direction: column;
    background: var(--bg);
    border-right: 1px solid var(--border);
}
.panel.overlay {
    position: absolute;
    inset: 0 auto 0 0;
    background: var(--surface);
    box-shadow: var(--float);
}
.head {
    flex: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 36px;
    padding: 0 8px 0 18px;
    border-bottom: 1px solid var(--border);
}
.title {
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.045em;
    text-transform: uppercase;
    color: var(--text3);
}
.pin {
    all: unset;
    box-sizing: border-box;
    width: 24px;
    height: 24px;
    display: grid;
    place-items: center;
    border-radius: 5px;
    color: var(--text2);
    cursor: pointer;
}
.pin:hover {
    background: var(--s2);
    color: var(--text);
}
.pin:focus-visible,
.resizer:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.resizer {
    position: absolute;
    inset: 0 -3px 0 auto;
    width: 7px;
    cursor: col-resize;
    touch-action: none;
}
.resizer::after {
    content: '';
    position: absolute;
    inset: 0 3px 0 auto;
    width: 1px;
    background: transparent;
    transition: background 0.1s;
}
.resizer:hover::after,
.resizer:focus-visible::after {
    background: var(--focus);
    width: 2px;
}
.slide-enter-active,
.slide-leave-active {
    transition:
        transform 0.12s ease-out,
        opacity 0.12s ease-out;
}
.slide-enter-from.overlay,
.slide-leave-to.overlay {
    transform: translateX(-12px);
    opacity: 0;
}
@media (prefers-reduced-motion: reduce) {
    .slide-enter-active,
    .slide-leave-active {
        transition: none;
    }
}
@media (max-width: 899.98px) {
    .sidebar-host {
        display: none;
    }
}
</style>
