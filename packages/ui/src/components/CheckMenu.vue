<script setup lang="ts">
import { computed, nextTick, ref } from 'vue';

// A button that opens a popover of checkboxes over a bitmask (each option is one bit). The popover is teleported to <body> with
// fixed positioning so a table header's overflow does not clip it.

export interface CheckOption {
    bit: number;
    label: string;
    hint?: string;
}

const props = defineProps<{
    modelValue: number;
    options: readonly CheckOption[];
    /** Accessible name of the popover. */
    label: string;
    heading?: string;
    note?: string;
    btnClass?: string;
    btnTitle?: string;
    /** Shows a Clear button (sets 0). */
    clearable?: boolean;
    /** Keeps at least one option checked. */
    required?: boolean;
    testid?: string;
}>();
const emit = defineEmits<{ 'update:modelValue': [v: number] }>();

const open = ref(false);
const btnEl = ref<HTMLButtonElement | null>(null);
const menuEl = ref<HTMLDivElement | null>(null);
const pos = ref({ top: 0, left: 0 });
const WIDTH = 248;

async function toggleMenu() {
    if (open.value) return close();
    const r = btnEl.value?.getBoundingClientRect();
    if (r) pos.value = { top: r.bottom + 4, left: Math.max(8, Math.min(r.left, window.innerWidth - WIDTH - 8)) };
    open.value = true;
    await nextTick();
    menuEl.value?.querySelector<HTMLInputElement>('input')?.focus();
}
function close() {
    open.value = false;
    btnEl.value?.focus();
}
const allBits = computed(() => props.options.reduce((m, o) => m | o.bit, 0));
/** With `required`, the last checked option cannot be unchecked. */
const locked = (bit: number) => !!props.required && (props.modelValue & allBits.value) === bit;
const toggle = (bit: number) => emit('update:modelValue', props.modelValue ^ bit);
</script>

<template>
    <span class="cm" @keydown.esc.stop="open && close()">
        <button
            ref="btnEl"
            :class="btnClass ?? 'btn'"
            type="button"
            aria-haspopup="dialog"
            :aria-expanded="open"
            :title="btnTitle"
            :aria-label="btnTitle"
            :data-testid="testid"
            @click.stop="toggleMenu"
        >
            <slot />
        </button>
        <Teleport v-if="open" to="body">
            <div class="cm-back" @mousedown="close"></div>
            <div
                ref="menuEl"
                class="floating cm-menu"
                role="dialog"
                :aria-label="label"
                :style="{ top: pos.top + 'px', left: pos.left + 'px', width: WIDTH + 'px' }"
                @keydown.esc.stop="close"
            >
                <div v-if="heading" class="cm-head">{{ heading }}</div>
                <label v-for="o in options" :key="o.bit" class="cm-opt" :title="o.hint">
                    <input class="cb" type="checkbox" :checked="(modelValue & o.bit) !== 0" :disabled="locked(o.bit)" @change="toggle(o.bit)" />
                    <span class="cm-l">{{ o.label }}</span>
                    <span v-if="o.hint" class="cm-hint muted">{{ o.hint }}</span>
                </label>
                <p v-if="note" class="cm-note muted">{{ note }}</p>
                <div class="cm-foot">
                    <button v-if="clearable" class="btn ghost sm" type="button" :disabled="!modelValue" @click="emit('update:modelValue', 0)">
                        Clear
                    </button>
                    <div class="grow"></div>
                    <button class="btn sm" type="button" @click="close">Done</button>
                </div>
            </div>
        </Teleport>
    </span>
</template>

<style scoped>
.cm {
    display: inline-flex;
    min-width: 0;
}
.cm-back {
    position: fixed;
    inset: 0;
    z-index: 40;
}
.cm-menu {
    position: fixed;
    z-index: 41;
    padding: 8px;
    font-size: 13px;
    font-weight: 400;
    color: var(--text);
    white-space: normal;
}
.cm-head {
    padding: 2px 6px 6px;
    font-size: 12px;
    font-weight: 600;
    color: var(--text2);
}
.cm-opt {
    display: grid;
    grid-template-columns: auto 1fr;
    column-gap: 8px;
    align-items: center;
    padding: 4px 6px;
    border-radius: 5px;
    cursor: pointer;
}
.cm-opt:hover {
    background: var(--hover);
}
.cm-opt:has(:focus-visible) {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.cm-hint {
    grid-column: 2;
    font-size: 11px;
}
.cm-note {
    margin: 6px 6px 0;
    font-size: 11px;
}
.cm-foot {
    display: flex;
    align-items: center;
    margin-top: 8px;
}
@media (pointer: coarse) {
    .cm-opt {
        min-height: 44px;
    }
}
</style>
