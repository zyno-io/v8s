<script setup lang="ts">
import type { ObjectApplyResult, ObjectDryRunResult, ObjectRef } from '@v8s/client';

import { computed, defineAsyncComponent, nextTick, onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';

import { newRequestId } from '@/lib/actions.ts';
import { getAgent, getNamespacesView, kinds } from '@/lib/agent.ts';
import { openObject } from '@/lib/drawer.ts';
import { errorText } from '@/lib/mutations.ts';
import { namespaceNames } from '@/lib/namespaces.ts';
import { gvrFromManifest, TEMPLATES, templateFor } from '@/lib/templates.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useTableStore } from '@/stores/table.ts';
import { useUiStore } from '@/stores/ui.ts';

const YamlEditor = defineAsyncComponent(() => import('./YamlEditor.vue'));

const ui = useUiStore();
const clusters = useClustersStore();
const table = useTableStore();
const router = useRouter();

const el = ref<HTMLDialogElement | null>(null);
const editor = ref<{ getValue(): string; setValue(t: string): void; focus(): void } | null>(null);

const BLANK = '';
const initialType = templateFor(table.currentGvr) ? table.currentGvr : BLANK;
const type = ref<string>(initialType);
const cluster = ref(clusters.activeIds[0] ?? '');
const namespace = ref(table.currentGvr ? (table.of(table.currentGvr).namespaces[0] ?? 'default') : 'default');
const nsOptions = computed(() => namespaceNames(getNamespacesView()));
const tpl = computed(() => templateFor(type.value));
const initial = ref(tpl.value ? tpl.value.yaml(namespace.value) : '# Paste one or more manifests, or pick a template above.\n');

const busy = ref(false);
const dry = ref<ObjectDryRunResult | null>(null);
const failure = ref('');
const created = ref(false);

function applyTemplate() {
    dry.value = null;
    failure.value = '';
    const t = tpl.value;
    initial.value = t ? t.yaml(namespace.value) : '# Paste one or more manifests.\n';
    editor.value?.setValue(initial.value);
}

onMounted(async () => {
    el.value?.showModal();
    await nextTick();
    editor.value?.focus();
});

function close() {
    ui.createOpen = false;
}

interface Resolved {
    yaml: string;
    ref: ObjectRef;
}

async function resolve(): Promise<Resolved | string> {
    const yaml = editor.value?.getValue() ?? initial.value;
    const { loadAll } = await import('js-yaml');
    let docs: unknown[];
    try {
        docs = loadAll(yaml).filter(d => d && typeof d === 'object');
    } catch (e) {
        return `YAML does not parse: ${e instanceof Error ? e.message.split('\n')[0] : String(e)}`;
    }
    if (!docs.length) return 'Add a manifest to create.';
    const first = docs[0] as { apiVersion?: string; kind?: string; metadata?: { name?: string; namespace?: string } };
    const gvr = gvrFromManifest(first, kinds.value) ?? tpl.value?.gvr;
    if (!gvr) return `Unknown type ${first.apiVersion ?? ''} ${first.kind ?? ''}. None of the selected clusters serves it.`;
    if (!cluster.value) return 'Choose a cluster.';
    return { yaml, ref: { cluster: cluster.value, gvr, namespace: first.metadata?.namespace || undefined, name: first.metadata?.name || '' } };
}

async function runDry(r: Resolved): Promise<boolean> {
    dry.value = await getAgent().call<ObjectDryRunResult>('object.dryRun', { ref: r.ref, yaml: r.yaml, mode: 'create' });
    return dry.value.ok;
}

async function dryRun() {
    busy.value = true;
    failure.value = '';
    try {
        const r = await resolve();
        if (typeof r === 'string') failure.value = r;
        else await runDry(r);
    } catch (e) {
        failure.value = errorText(e);
    } finally {
        busy.value = false;
    }
}

let requestId: string | null = null;
async function create() {
    busy.value = true;
    failure.value = '';
    try {
        const r = await resolve();
        if (typeof r === 'string') {
            failure.value = r;
            return;
        }
        if (!(await runDry(r))) return;
        requestId ??= newRequestId();
        const res = await getAgent().call<ObjectApplyResult>('object.apply', { requestId, ref: r.ref, yaml: r.yaml, mode: 'create' });
        requestId = null;
        created.value = true;
        const n = res.applied?.length ?? 1;
        ui.toast(
            'success',
            `Created ${n === 1 ? (res.applied?.[0]?.name ?? r.ref.name) : n + ' objects'}${res.usedContext ? ` as ${res.usedContext}` : ''}`
        );
        close();
        if (n === 1 && res.applied?.[0]) void openObject(router, { ...res.applied[0], cluster: r.ref.cluster });
    } catch (e) {
        failure.value = errorText(e);
        requestId = null;
    } finally {
        busy.value = false;
    }
}
</script>

<template>
    <dialog ref="el" class="dlg floating" aria-labelledby="create-title" @cancel.prevent="close" @click.self="close">
        <div class="box">
            <header>
                <h2 id="create-title">Create</h2>
                <div class="grow"></div>
                <button class="btn ghost sm" type="button" aria-label="Close" @click="close">Close</button>
            </header>
            <div class="controls">
                <label class="lab"
                    >Type
                    <select v-model="type" class="sel" data-testid="create-type" @change="applyTemplate">
                        <option :value="BLANK">Blank (paste YAML)</option>
                        <option v-for="t in TEMPLATES" :key="t.gvr" :value="t.gvr">{{ t.label }}</option>
                    </select>
                </label>
                <label class="lab"
                    >Cluster
                    <select v-model="cluster" class="sel" data-testid="create-cluster">
                        <option v-for="id in clusters.activeIds" :key="id" :value="id">{{ clusters.nameById.get(id) ?? id }}</option>
                    </select>
                </label>
                <label v-if="tpl?.namespaced" class="lab"
                    >Namespace
                    <span class="field"
                        ><input v-model="namespace" list="ns-options" type="text" aria-label="Namespace" @change="applyTemplate"
                    /></span>
                    <datalist id="ns-options"><option v-for="n in nsOptions" :key="n" :value="n"></option></datalist>
                </label>
            </div>
            <div class="editor"><YamlEditor ref="editor" :initial="initial" :read-only="false" label="Manifest YAML" /></div>
            <div v-if="failure" class="notice danger" role="alert"><div>{{ failure }}</div></div>
            <template v-if="dry">
                <div v-for="w in dry.warnings ?? []" :key="w" class="notice warn" role="note"><div>{{ w }}</div></div>
                <div v-if="!dry.ok" class="notice danger" role="alert">
                    <div><b>The server rejected this manifest.</b> {{ dry.error?.message }}</div>
                </div>
                <div v-else class="notice ok" role="status" data-testid="dry-ok"><div>Server dry run passed.</div></div>
            </template>
            <footer>
                <div class="grow"></div>
                <button class="btn" type="button" :disabled="busy" data-testid="dry-run" @click="dryRun">Dry run</button>
                <button class="btn primary" type="button" :disabled="busy" data-testid="create-submit" @click="create">
                    {{ busy ? 'Working…' : 'Create' }}
                </button>
            </footer>
        </div>
    </dialog>
</template>

<style scoped>
.dlg {
    padding: 0;
    width: min(760px, calc(100vw - 24px));
    max-height: calc(100vh - 32px);
    color: var(--text);
    border-radius: 12px;
}
.dlg::backdrop {
    background: rgba(12, 13, 16, 0.45);
}
.box {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 16px;
    height: min(680px, calc(100vh - 34px));
}
header,
footer {
    display: flex;
    align-items: center;
    gap: 8px;
}
h2 {
    margin: 0;
    font-size: 16px;
}
.controls {
    display: flex;
    gap: 12px;
    flex-wrap: wrap;
}
.lab {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.045em;
    text-transform: uppercase;
    color: var(--text3);
}
.sel {
    height: 32px;
    min-width: 160px;
    border: 1px solid var(--bstrong);
    border-radius: 8px;
    background: var(--surface);
    color: var(--text);
    font-family: inherit;
    font-size: 13px;
    padding: 0 8px;
    text-transform: none;
    letter-spacing: 0;
    font-weight: 400;
}
.editor {
    flex: 1 1 0;
    min-height: 200px;
}
</style>
