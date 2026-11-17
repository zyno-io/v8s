<script setup lang="ts">
import type { ObjectRef } from '@v8s/client';

import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue';

import type { StartResult } from '@/lib/portforward.ts';

import { errorText } from '@/lib/mutations.ts';
import { browserUrl, isHttpish, portsOf, saveForward, startForward } from '@/lib/portforward.ts';
import { useClustersStore } from '@/stores/clusters.ts';

const props = defineProps<{ target: ObjectRef; parsed: Record<string, unknown> | null }>();
const emit = defineEmits<{ close: [] }>();

const clusters = useClustersStore();
const el = ref<HTMLDialogElement | null>(null);
const options = computed(() => portsOf(props.target.gvr, props.parsed));
const CUSTOM = 'custom';
const choice = ref<string>(options.value.length ? String(options.value[0].port) : CUSTOM);
const custom = ref('');
const save = ref(false);
const busy = ref(false);
const err = ref('');
const result = ref<StartResult | null>(null);
const startedPort = ref(0);
let returnFocus: HTMLElement | null = null;

const writeCtx = computed(() => clusters.writeContextOf(props.target.cluster));
const port = computed(() => {
    if (choice.value !== CUSTOM) return Number(choice.value);
    const raw = custom.value.trim();
    const n = Number(raw);
    return raw !== '' && Number.isInteger(n) && n >= 1 && n <= 65535 ? n : 0;
});
const selected = computed(() => options.value.find(o => o.port === startedPort.value));
const httpish = computed(() => isHttpish(selected.value ?? { port: startedPort.value }));

onMounted(async () => {
    returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    await nextTick();
    if (!el.value?.open) el.value?.showModal();
    el.value?.querySelector<HTMLElement>('input')?.focus();
});
onBeforeUnmount(() => {
    if (el.value?.open) el.value.close();
    returnFocus?.focus?.();
});

async function start() {
    if (!port.value || busy.value) return;
    busy.value = true;
    err.value = '';
    try {
        const p = port.value;
        const r = await startForward(props.target, p);
        startedPort.value = p;
        result.value = r;
        if (save.value) {
            try {
                await saveForward({ ref: props.target, port: p });
            } catch (e) {
                err.value = `Forwarding, but saving failed: ${errorText(e)}`;
            }
        }
        await nextTick();
        el.value?.querySelector<HTMLElement>('[data-testid="pf-open"], button.primary')?.focus();
    } catch (e) {
        err.value = errorText(e);
    } finally {
        busy.value = false;
    }
}
</script>

<template>
    <dialog
        ref="el"
        class="dlg floating"
        aria-labelledby="pf-title"
        data-testid="pf-dialog"
        @cancel.prevent="emit('close')"
        @click.self="emit('close')"
    >
        <form @submit.prevent="start">
            <h2 id="pf-title">Port forward: {{ target.name }}</h2>
            <template v-if="!result">
                <fieldset>
                    <legend class="muted">Remote port</legend>
                    <label v-for="o in options" :key="o.port" class="opt">
                        <input v-model="choice" type="radio" name="pf-port" :value="String(o.port)" :data-testid="`pf-port-${o.port}`" />
                        <span class="mono">{{ o.port }}</span>
                        <span v-if="o.name" class="muted">{{ o.name }}</span>
                        <span v-if="o.protocol" class="muted">{{ o.protocol }}</span>
                        <span v-if="o.containerName" class="muted">({{ o.containerName }})</span>
                    </label>
                    <label class="opt">
                        <input v-model="choice" type="radio" name="pf-port" :value="CUSTOM" data-testid="pf-port-custom" />
                        <span>Custom port</span>
                        <span class="field"
                            ><input
                                v-model="custom"
                                type="number"
                                min="1"
                                max="65535"
                                step="1"
                                inputmode="numeric"
                                aria-label="Custom port"
                                @focus="choice = CUSTOM"
                        /></span>
                    </label>
                </fieldset>
                <label class="opt"><input v-model="save" type="checkbox" /> Save this forward</label>
                <p v-if="writeCtx" class="muted" data-testid="pf-runas">Runs as write context <b class="mono">{{ writeCtx }}</b>.</p>
                <p v-if="err" class="err" role="alert">{{ err }}</p>
                <div class="foot">
                    <button class="btn" type="button" @click="emit('close')">Cancel</button>
                    <button class="btn primary" type="submit" :disabled="!port || busy" data-testid="pf-start">
                        {{ busy ? 'Starting…' : 'Start' }}
                    </button>
                </div>
            </template>
            <template v-else>
                <p role="status" data-testid="pf-result">
                    Forwarding <b class="mono">127.0.0.1:{{ result.localPort }}</b> -> <span class="mono">{{ target.name }}:{{ startedPort }}</span>
                </p>
                <p v-if="err" class="err" role="alert">{{ err }}</p>
                <div class="foot">
                    <a v-if="httpish" class="btn" :href="browserUrl(result.localPort)" target="_blank" rel="noopener" data-testid="pf-open"
                        >Open in browser</a
                    >
                    <button class="btn primary" type="button" @click="emit('close')">Done</button>
                </div>
            </template>
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
    word-break: break-word;
}
p {
    margin: 0;
    font-size: 13px;
    word-break: break-word;
}
fieldset {
    border: 0;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
}
.opt {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    min-height: 28px;
}
.err {
    color: var(--danger, #c0392b);
}
.foot {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
}
@media (pointer: coarse) {
    .opt {
        min-height: 44px;
    }
}
</style>
