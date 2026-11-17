<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';

import type { DialogState } from '@/stores/ui.ts';

import HoldButton from '@/components/HoldButton.vue';
import { isPhone } from '@/lib/viewport.ts';
import { useUiStore } from '@/stores/ui.ts';

const ui = useUiStore();
const el = ref<HTMLDialogElement | null>(null);
const typed = ref('');
const num = ref<string | number>('');
const busy = ref(false);
const err = ref('');
let returnFocus: HTMLElement | null = null;

const d = computed(() => ui.dialog);
// Phones never run a destructive action on a single tap: hold to confirm (a typed phrase already is a deliberate step).
// Scale and rollback always hold on a phone; a confirm holds when it is destructive or asks to (`hold`).
const holdMode = computed(() => {
    const v = d.value;
    if (!isPhone.value || !v || v.phrase) return false;
    return v.type !== 'confirm' || v.danger || !!v.hold;
});

// Each confirm dialog is cancelled at most once; a dialog that ran, or was already cancelled, is never cancelled again.
const settled = new WeakSet<DialogState>();
function cancelOf(v: DialogState | null | undefined) {
    if (!v || settled.has(v)) return;
    settled.add(v);
    if (v.type === 'confirm') v.onCancel?.();
}

watch(d, async (v, old) => {
    // Replaced by another dialog while open: the old one was dismissed.
    if (old && old !== v) cancelOf(old);
    typed.value = '';
    err.value = '';
    busy.value = false;
    if (v) {
        if (!old) returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
        num.value =
            v.type === 'scale' ? (v.current == null ? '' : String(v.current)) : v.type === 'rollback' ? String(Math.max(0, v.current - 1) || '') : '';
        await nextTick();
        if (!el.value?.open) el.value?.showModal();
        el.value?.querySelector<HTMLElement>('input, button.primary, button.danger')?.focus();
    } else {
        if (el.value?.open) el.value.close();
        returnFocus?.focus?.();
    }
});

function close(ran = false) {
    const v = ui.dialog;
    if (ran && v) settled.add(v);
    else cancelOf(v);
    ui.dialog = null;
}

const canRun = computed(() => {
    const v = d.value;
    if (!v || busy.value) return false;
    if (v.phrase && typed.value.trim() !== v.phrase) return false;
    if (v.type === 'confirm') return true;
    const raw = String(num.value ?? '').trim();
    const n = Number(raw);
    return raw !== '' && Number.isInteger(n) && n >= 0;
});

async function run(fromHold = false) {
    const v = d.value;
    if (!v || !canRun.value) return;
    // Enter in a field must not submit a dialog that needs the hold.
    if (holdMode.value && !fromHold) return;
    busy.value = true;
    try {
        if (v.type === 'confirm') await v.run();
        else await v.run(Number(num.value));
        close(true);
    } catch (e) {
        err.value = e instanceof Error ? e.message : String(e);
        busy.value = false;
    }
}
</script>

<template>
    <dialog ref="el" class="dlg floating" aria-labelledby="dlg-title" @cancel.prevent="close()" @click.self="close()">
        <form v-if="d" method="dialog" @submit.prevent="run()">
            <template v-if="d.type === 'confirm'">
                <h2 id="dlg-title">{{ d.title }}</h2>
                <p>{{ d.body }}</p>
            </template>
            <template v-else-if="d.type === 'scale'">
                <h2 id="dlg-title">Scale {{ d.targets.length === 1 ? d.targets[0].name : d.targets.length + ' objects' }}</h2>
                <label class="phrase"
                    >Replicas
                    <span class="field"
                        ><input v-model="num" type="number" min="0" step="1" inputmode="numeric" required aria-label="Replicas"
                    /></span>
                </label>
            </template>
            <template v-else>
                <h2 id="dlg-title">Roll back {{ d.target.name }}</h2>
                <p>Restores the pod template of an earlier revision (a ReplicaSet's <span class="mono">deployment.kubernetes.io/revision</span>).</p>
                <label class="phrase"
                    >Revision
                    <span class="field"><input v-model="num" type="number" min="0" step="1" inputmode="numeric" aria-label="Revision" /></span>
                </label>
            </template>
            <p v-if="d.runAs" class="runas" data-testid="runas">{{ d.runAs }}</p>
            <p v-if="d.note" class="note tag warning">{{ d.note }}</p>
            <label v-if="d.phrase" class="phrase"
                >Type <b class="mono">{{ d.phrase }}</b> to confirm
                <span class="field"><input v-model="typed" type="text" autocomplete="off" spellcheck="false" aria-label="Confirmation text" /></span>
            </label>
            <p v-if="err" class="err" role="alert">{{ err }}</p>
            <div class="foot">
                <button class="btn" type="button" @click="close()">Cancel</button>
                <HoldButton
                    v-if="holdMode"
                    :label="d.type === 'confirm' ? d.confirmLabel : d.type === 'scale' ? 'Scale' : 'Roll back'"
                    :danger="d.type === 'confirm' && d.danger"
                    :disabled="!canRun"
                    @confirm="run(true)"
                />
                <button v-else class="btn" :class="d.type === 'confirm' && d.danger ? 'danger' : 'primary'" type="submit" :disabled="!canRun">
                    {{ busy ? 'Working…' : d.type === 'confirm' ? d.confirmLabel : d.type === 'scale' ? 'Scale' : 'Roll back' }}
                </button>
            </div>
        </form>
    </dialog>
</template>

<style scoped>
.dlg {
    padding: 0;
    width: min(440px, calc(100vw - 24px));
    color: var(--text);
    border-radius: 12px;
}
.dlg::backdrop {
    background: rgba(12, 13, 16, 0.45);
}
form {
    padding: 18px 18px 14px;
    display: flex;
    flex-direction: column;
    gap: 12px;
}
h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
}
p {
    margin: 0;
    font-size: 13px;
    color: var(--text2);
    word-break: break-word;
}
.note {
    align-self: flex-start;
    height: auto;
    padding: 3px 7px;
    white-space: normal;
}
.phrase {
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 13px;
}
.err {
    color: var(--danger);
}
.runas {
    font-weight: 500;
    color: var(--text);
}
.foot {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
}
</style>
