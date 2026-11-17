<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';

import { useUiStore } from '@/stores/ui.ts';

const ui = useUiStore();
const el = ref<HTMLDialogElement | null>(null);
const typed = ref('');
const num = ref<string | number>('');
const busy = ref(false);
const err = ref('');
let returnFocus: HTMLElement | null = null;

const d = computed(() => ui.dialog);

watch(d, async v => {
    typed.value = '';
    err.value = '';
    busy.value = false;
    if (v) {
        returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
        num.value = v.type === 'scale' ? String(v.current) : v.type === 'rollback' ? String(Math.max(0, v.current - 1) || '') : '';
        await nextTick();
        if (!el.value?.open) el.value?.showModal();
        el.value?.querySelector<HTMLElement>('input, button.primary, button.danger')?.focus();
    } else {
        if (el.value?.open) el.value.close();
        returnFocus?.focus?.();
    }
});

function close() {
    ui.dialog = null;
}

const canRun = computed(() => {
    const v = d.value;
    if (!v || busy.value) return false;
    if (v.type === 'confirm') return !v.phrase || typed.value.trim() === v.phrase;
    const raw = String(num.value ?? '').trim();
    const n = Number(raw);
    return raw !== '' && Number.isInteger(n) && n >= 0;
});

async function run() {
    const v = d.value;
    if (!v || !canRun.value) return;
    busy.value = true;
    try {
        if (v.type === 'confirm') await v.run();
        else await v.run(Number(num.value));
        close();
    } catch (e) {
        err.value = e instanceof Error ? e.message : String(e);
        busy.value = false;
    }
}
</script>

<template>
    <dialog ref="el" class="dlg floating" aria-labelledby="dlg-title" @cancel.prevent="close" @click.self="close">
        <form v-if="d" method="dialog" @submit.prevent="run">
            <template v-if="d.type === 'confirm'">
                <h2 id="dlg-title">{{ d.title }}</h2>
                <p>{{ d.body }}</p>
                <p v-if="d.note" class="note tag warning">{{ d.note }}</p>
                <label v-if="d.phrase" class="phrase"
                    >Type <b class="mono">{{ d.phrase }}</b> to confirm
                    <span class="field"
                        ><input v-model="typed" type="text" autocomplete="off" spellcheck="false" aria-label="Confirmation text"
                    /></span>
                </label>
            </template>
            <template v-else-if="d.type === 'scale'">
                <h2 id="dlg-title">Scale {{ d.targets.length === 1 ? d.targets[0].name : d.targets.length + ' objects' }}</h2>
                <label class="phrase"
                    >Replicas
                    <span class="field"><input v-model="num" type="number" min="0" step="1" inputmode="numeric" aria-label="Replicas" /></span>
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
            <p v-if="err" class="err" role="alert">{{ err }}</p>
            <div class="foot">
                <button class="btn" type="button" @click="close">Cancel</button>
                <button class="btn" :class="d.type === 'confirm' && d.danger ? 'danger' : 'primary'" type="submit" :disabled="!canRun">
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
.foot {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
}
</style>
