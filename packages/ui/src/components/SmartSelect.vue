<script setup lang="ts" generic="T extends string | number | boolean | null">
/*
 * A searchable dropdown replacing the native <select>. Modeled on VfSmartSelect / VfEzSmartSelect from
 * @signal24/vue-foundation (https://github.com/zyno-io/vue-foundation), Copyright (c) Signal 24, MIT licensed.
 * Vendored and rewritten rather than imported: the package ships as one bundle that pulls in lodash, mark.js, date-fns and
 * a global stylesheet (measured at +101 kB raw / +36 kB gzip on the chunk that used it).
 *
 * Differences from the original: no lodash or mark.js (small native helpers in lib/smart-select.ts; matches are drawn as
 * <mark> runs of text, never v-html, because option text is cluster data); the list renders only the rows in view; it lives in a
 * top-layer popover, so `contain:` / `container-type:` ancestors and modal <dialog>s cannot mis-position or cover it; touch
 * rows are 44px and phone widths get a bottom sheet. Nothing is computed while closed.
 */
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue';

import {
    edgeActive,
    filterOptions,
    moveActive,
    placePopup,
    queryTerms,
    scrollToRow,
    SEARCH_MIN_OPTIONS,
    splitHighlight,
    typeahead,
    visibleRange,
    type Placement,
    type SelectOption
} from '@/lib/smart-select.ts';

defineOptions({ inheritAttrs: false });

const props = withDefaults(
    defineProps<{
        modelValue: T;
        options: readonly SelectOption<T>[];
        /** Accessible name; the current value is appended so a screen reader hears both, as with a native select. */
        ariaLabel?: string;
        /** Shown when no option matches the value. */
        placeholder?: string;
        disabled?: boolean;
        /** Fill the parent's width instead of fitting the label. */
        block?: boolean;
        size?: 'md' | 'sm';
        /** CSS max-width of the trigger. */
        maxWidth?: string;
        /** 'auto' adds a search field above SEARCH_MIN_OPTIONS options. */
        search?: boolean | 'auto';
        /** Placeholder of the search field. */
        searchPlaceholder?: string;
    }>(),
    { placeholder: '', size: 'md', search: 'auto', searchPlaceholder: 'Search' }
);
const emit = defineEmits<{ 'update:modelValue': [value: T]; change: [value: T] }>();

const MAX_HEIGHT = 288;
const PAGE = 8;

const id = useId();
const listId = `${id}-list`;
const open = ref(false);
const query = ref('');
const active = ref(-1);
const scrollTop = ref(0);
const place = ref<Placement | null>(null);
const sheet = ref(false);
const rowH = ref(32);
const host = ref<HTMLElement | 'body'>('body');

const trigger = ref<HTMLButtonElement | null>(null);
const layer = ref<HTMLElement | null>(null);
const listEl = ref<HTMLElement | null>(null);
const searchEl = ref<HTMLInputElement | null>(null);

const selected = computed(() => props.options.find(o => o.value === props.modelValue));
const label = computed(() => selected.value?.label ?? props.placeholder);
const accName = computed(() => (props.ariaLabel ? `${props.ariaLabel}: ${selected.value?.label ?? 'none'}` : undefined));
const searchable = computed(() => (props.search === 'auto' ? props.options.length >= SEARCH_MIN_OPTIONS : props.search));

// Nothing below runs while closed: `open` gates the filter, and the list only mounts when open.
const items = computed<number[]>(() => (open.value ? filterOptions(props.options, query.value) : []));
const terms = computed(() => queryTerms(query.value));
const range = computed(() => visibleRange(scrollTop.value, listViewport(), rowH.value, items.value.length));
const rows = computed(() => {
    const [s, e] = range.value;
    const out: { pos: number; idx: number; opt: SelectOption<T> }[] = [];
    for (let pos = s; pos < e; pos++) out.push({ pos, idx: items.value[pos], opt: props.options[items.value[pos]] });
    return out;
});
const listHeight = computed(() => (items.value.length ? Math.min(items.value.length * rowH.value + 8, MAX_HEIGHT) : 0));
const activeId = computed(() => (active.value >= 0 && open.value ? `${id}-o${items.value[active.value]}` : undefined));

function listViewport(): number {
    return listEl.value?.clientHeight || MAX_HEIGHT;
}
const optDisabled = (i: number) => !!props.options[i]?.disabled;

function measure() {
    const el = trigger.value;
    if (!el) return;
    const searchH = searchable.value ? rowH.value + 9 : 0;
    const wanted = Math.min(props.options.length * rowH.value + 8, MAX_HEIGHT) + searchH + 2;
    if (sheet.value) {
        place.value = { left: 0, width: window.innerWidth, bottom: 0, maxHeight: Math.min(wanted + 8, window.innerHeight * 0.7), above: true };
        return;
    }
    place.value = placePopup({
        rect: el.getBoundingClientRect(),
        viewport: { width: window.innerWidth, height: window.innerHeight },
        wanted,
        minWidth: 180
    });
}

async function openList(seed = '') {
    if (open.value || props.disabled) return;
    const coarse = window.matchMedia('(pointer: coarse)').matches;
    sheet.value = window.matchMedia('(max-width: 639.98px)').matches;
    rowH.value = coarse || sheet.value ? 44 : 32;
    // A modal <dialog> makes everything outside it inert, so the popup goes inside the dialog the trigger sits in.
    host.value = trigger.value?.closest('dialog') ?? 'body';
    opening = true;
    query.value = searchable.value ? seed : '';
    scrollTop.value = 0;
    measure();
    open.value = true;
    const list = filterOptions(props.options, query.value);
    const cur = list.findIndex(i => props.options[i].value === props.modelValue);
    active.value = cur >= 0 ? cur : edgeActive(list, optDisabled, false);
    await nextTick();
    // Typing to open: a search seed highlights its first match; a short list jumps by type-ahead (buffer kept, so `ga` works).
    if (seed && searchable.value) active.value = edgeActive(list, optDisabled, false);
    else if (seed) typeAhead(seed);
    // popover="manual": top layer, above a modal dialog and out of every `contain:` ancestor's reach.
    if (layer.value && typeof layer.value.showPopover === 'function') {
        try {
            layer.value.showPopover();
        } catch {
            /* already shown */
        }
    }
    const el = listEl.value;
    if (el) {
        el.scrollTop = Math.max(0, active.value * rowH.value - el.clientHeight / 2 + rowH.value / 2);
        scrollTop.value = el.scrollTop;
    }
    (searchEl.value ?? listEl.value)?.focus({ preventScroll: true });
    opening = false;
    window.addEventListener('resize', reposition);
    window.addEventListener('scroll', reposition, true);
}

let raf = 0;
function reposition(e?: Event) {
    if (e && e.target instanceof Node && layer.value?.contains(e.target)) return;
    if (raf) return;
    raf = requestAnimationFrame(() => {
        raf = 0;
        if (open.value) measure();
    });
}

function closeList(refocus = true) {
    if (!open.value) return;
    open.value = false;
    // A stale query must not leak into the next open (it would re-filter and move the highlight).
    query.value = '';
    window.removeEventListener('resize', reposition);
    window.removeEventListener('scroll', reposition, true);
    if (refocus) trigger.value?.focus({ preventScroll: true });
}

function choose(idx: number) {
    const o = props.options[idx];
    if (!o || o.disabled) return;
    closeList();
    if (o.value !== props.modelValue) {
        emit('update:modelValue', o.value);
        emit('change', o.value);
    }
}

function setActive(pos: number) {
    if (pos < 0) return;
    active.value = pos;
    const el = listEl.value;
    if (el) {
        const next = scrollToRow(pos, el.scrollTop, el.clientHeight - 8, rowH.value);
        if (next !== el.scrollTop) {
            el.scrollTop = next;
            scrollTop.value = next;
        }
    }
}
const jumpTo = setActive;

// `opening` keeps this from overwriting the highlight openList sets for the current value.
let opening = false;
watch(query, () => {
    if (!open.value || opening) return;
    // The filter changed: back to the top with the first enabled match active.
    scrollTop.value = 0;
    if (listEl.value) listEl.value.scrollTop = 0;
    active.value = edgeActive(items.value, optDisabled, false);
});
watch(
    () => props.disabled,
    d => d && closeList(false)
);
onBeforeUnmount(() => {
    window.removeEventListener('resize', reposition);
    window.removeEventListener('scroll', reposition, true);
    if (raf) cancelAnimationFrame(raf);
});

let buffer = '';
let bufferTimer: ReturnType<typeof setTimeout> | undefined;

function onListKey(e: KeyboardEvent) {
    if (e.isComposing || e.keyCode === 229) return; // an IME commit, not a command
    const list = items.value;
    let handled = true;
    switch (e.key) {
        case 'ArrowDown':
            setActive(moveActive(list, optDisabled, active.value, 1));
            break;
        case 'ArrowUp':
            setActive(moveActive(list, optDisabled, active.value, -1));
            break;
        case 'PageDown':
            setActive(moveActive(list, optDisabled, active.value, PAGE));
            break;
        case 'PageUp':
            setActive(moveActive(list, optDisabled, active.value, -PAGE));
            break;
        case 'Home':
            setActive(edgeActive(list, optDisabled, false));
            break;
        case 'End':
            setActive(edgeActive(list, optDisabled, true));
            break;
        case 'Enter':
            if (active.value >= 0) choose(list[active.value]);
            break;
        case 'Escape':
            closeList();
            break;
        case 'Tab':
            // Focus goes back to the trigger first, so Tab continues from there.
            closeList();
            handled = false;
            break;
        case ' ':
            if (searchable.value) handled = false;
            else if (buffer) typeAhead(' ');
            else if (active.value >= 0) choose(list[active.value]);
            break;
        default:
            handled = false;
            if (!searchable.value && e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
                typeAhead(e.key);
                handled = true;
            }
    }
    if (handled) {
        e.preventDefault();
        e.stopPropagation();
    } else if (e.key !== 'Tab') {
        // Typing into the search field must not reach the page's shortcuts (j, k, /, ...).
        e.stopPropagation();
    }
}

function typeAhead(ch: string) {
    buffer += ch;
    clearTimeout(bufferTimer);
    bufferTimer = setTimeout(() => (buffer = ''), 700);
    jumpTo(typeahead(items.value, props.options, buffer, active.value));
}

defineExpose({ focus: () => trigger.value?.focus() });

function onTriggerKey(e: KeyboardEvent) {
    if (e.defaultPrevented || props.disabled || e.isComposing || e.keyCode === 229) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        void openList();
    } else if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault();
        void openList(e.key);
    }
}

function onLayerDown(e: MouseEvent) {
    // A press on the backdrop (not the panel) dismisses.
    if (e.target === layer.value) closeList();
}

function onScroll(e: Event) {
    scrollTop.value = (e.target as HTMLElement).scrollTop;
}

const panelStyle = computed(() => {
    const p = place.value;
    if (!p) return {};
    return {
        left: p.left + 'px',
        width: p.width + 'px',
        top: p.top !== undefined ? p.top + 'px' : undefined,
        bottom: p.bottom !== undefined ? p.bottom + 'px' : undefined,
        maxHeight: p.maxHeight + 'px',
        '--row-h': rowH.value + 'px'
    };
});
</script>

<template>
    <button
        v-bind="$attrs"
        ref="trigger"
        type="button"
        class="ss"
        :class="{ block, sm: size === 'sm', empty: !selected, open }"
        data-select
        :data-value="modelValue === null ? undefined : String(modelValue)"
        aria-haspopup="listbox"
        :aria-expanded="open"
        :aria-controls="open ? listId : undefined"
        :aria-label="accName"
        :disabled="disabled"
        :style="maxWidth ? { maxWidth } : undefined"
        @click="open ? closeList() : openList()"
        @keydown="onTriggerKey"
    >
        <span class="ss-label">{{ label }}</span>
        <svg class="ss-caret" width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
            <path d="M4 6l4 4 4-4" />
        </svg>
    </button>
    <Teleport v-if="open" :to="host">
        <div ref="layer" popover="manual" class="ss-layer" :class="{ sheet }" @mousedown="onLayerDown">
            <div class="ss-panel floating" :class="{ sheet, above: place?.above }" :style="panelStyle" @keydown="onListKey">
                <input
                    v-if="searchable"
                    ref="searchEl"
                    v-model="query"
                    class="ss-search"
                    type="text"
                    role="combobox"
                    aria-autocomplete="list"
                    aria-expanded="true"
                    :aria-controls="listId"
                    :aria-activedescendant="activeId"
                    :aria-label="(ariaLabel ?? 'Options') + ' search'"
                    :placeholder="searchPlaceholder"
                    autocomplete="off"
                    autocapitalize="off"
                    spellcheck="false"
                    enterkeyhint="done"
                    data-1p-ignore
                />
                <div
                    :id="listId"
                    ref="listEl"
                    class="ss-list"
                    role="listbox"
                    :aria-label="ariaLabel"
                    :tabindex="searchable ? undefined : -1"
                    :aria-activedescendant="searchable ? undefined : activeId"
                    :style="{ height: listHeight + 'px' }"
                    @scroll.passive="onScroll"
                >
                    <div class="ss-sizer" :style="{ height: items.length * rowH + 8 + 'px' }">
                        <div
                            v-for="r in rows"
                            :id="`${id}-o${r.idx}`"
                            :key="r.idx"
                            class="ss-opt"
                            :class="{ active: r.pos === active, sel: r.opt.value === modelValue, dis: r.opt.disabled }"
                            role="option"
                            :aria-selected="r.opt.value === modelValue"
                            :aria-disabled="r.opt.disabled || undefined"
                            :aria-setsize="items.length"
                            :aria-posinset="r.pos + 1"
                            :title="r.opt.title"
                            :data-testid="r.opt.testid"
                            :style="{ top: r.pos * rowH + 4 + 'px' }"
                            @mousemove="active !== r.pos && !r.opt.disabled && (active = r.pos)"
                            @mousedown.prevent
                            @click="choose(r.idx)"
                        >
                            <span class="ss-text"
                                ><template v-for="(s, i) in splitHighlight(r.opt.label, terms)" :key="i"
                                    ><mark v-if="s.hit">{{ s.text }}</mark><template v-else>{{ s.text }}</template></template
                                ></span
                            >
                            <span v-if="r.opt.subtitle" class="ss-sub">{{ r.opt.subtitle }}</span>
                            <svg
                                v-if="r.opt.value === modelValue"
                                class="ss-check"
                                width="12"
                                height="12"
                                viewBox="0 0 16 16"
                                fill="none"
                                stroke="currentColor"
                                stroke-width="1.8"
                                aria-hidden="true"
                            >
                                <path d="M3 8.5l3.2 3L13 4.5" />
                            </svg>
                        </div>
                    </div>
                </div>
                <div v-if="!items.length" class="ss-none" role="status">{{ options.length ? 'No matches' : 'No options' }}</div>
            </div>
        </div>
    </Teleport>
</template>

<style scoped>
.ss {
    display: inline-flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    height: 32px;
    min-width: 0;
    max-width: 100%;
    padding: 0 8px 0 10px;
    border: 1px solid var(--bstrong);
    border-radius: 8px;
    background: var(--surface);
    box-shadow: var(--rest);
    color: var(--text);
    font: inherit;
    font-size: 13px;
    font-weight: 500;
    line-height: 1;
    text-align: left;
    white-space: nowrap;
    cursor: pointer;
}
.ss.sm {
    height: 26px;
    padding: 0 6px 0 8px;
    border-radius: 5px;
    font-size: 12px;
}
.ss.block {
    display: flex;
    width: 100%;
}
.ss.empty .ss-label {
    color: var(--text3);
}
.ss-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
}
.ss-caret {
    flex: none;
    color: var(--text3);
}
.ss:hover:not([disabled]) {
    border-color: var(--text3);
}
.ss:focus-visible,
.ss.open {
    outline: 0;
    border-color: var(--focus);
    box-shadow: 0 0 0 3px var(--accent-soft);
}
.ss[disabled] {
    opacity: 0.55;
    cursor: default;
}

/* The layer fills the viewport inside the top layer; its press target is the dismiss backdrop. */
.ss-layer {
    position: fixed;
    inset: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    padding: 0;
    border: 0;
    background: transparent;
    overflow: hidden;
    color: var(--text);
    font-size: 13px;
}
.ss-layer.sheet {
    background: rgb(0 0 0 / 0.35);
}
.ss-panel {
    position: absolute;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    box-sizing: border-box;
}
.ss-panel.sheet {
    border-radius: 14px 14px 0 0;
    padding-bottom: env(safe-area-inset-bottom);
}
.ss-search {
    flex: none;
    box-sizing: border-box;
    height: calc(var(--row-h) + 8px);
    padding: 0 12px;
    border: 0;
    border-bottom: 1px solid var(--border);
    background: transparent;
    color: var(--text);
    font: inherit;
    font-size: 13px;
    outline: 0;
}
.ss-search::placeholder {
    color: var(--text3);
}
.ss-list {
    /* Shrinks when the popup is capped (short viewport, sheet with a safe-area inset) so the bottom rows stay reachable by scrolling. */
    flex: 0 1 auto;
    min-height: 0;
    overflow-y: auto;
    overscroll-behavior: contain;
    outline: 0;
}
.ss-sizer {
    position: relative;
}
.ss-opt {
    position: absolute;
    left: 4px;
    right: 4px;
    display: flex;
    align-items: center;
    gap: 8px;
    height: var(--row-h);
    padding: 0 8px;
    border-radius: 5px;
    cursor: pointer;
    white-space: nowrap;
}
.ss-opt.active {
    background: var(--hover);
}
.ss-opt.sel {
    color: var(--accent);
    font-weight: 600;
}
.ss-opt.dis {
    color: var(--text3);
    cursor: default;
}
.ss-text {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
}
.ss-sub {
    flex: none;
    font-size: 11px;
    color: var(--text3);
}
.ss-check {
    flex: none;
}
.ss-list:focus-visible .ss-opt.active,
.ss-search:focus-visible ~ .ss-list .ss-opt.active {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
mark {
    background: var(--accent-soft);
    color: inherit;
    border-radius: 2px;
}
.ss-none {
    padding: 10px 12px;
    color: var(--text3);
}
@media (pointer: coarse) {
    .ss {
        min-height: 44px;
    }
}
</style>
