<script setup lang="ts">
import type { ObjectPayload, ObjectRef } from '@v8s/client';

import { computed, ref } from 'vue';

import type { ParsedObject } from '@/lib/object-session.ts';

import { clusterColor } from '@/lib/colors.ts';
import { formatAge } from '@/lib/format.ts';
import { conditionsOf, conditionTone, GITOPS_LABEL, keyFields, objectStatus, stringMap } from '@/lib/overview.ts';

const props = defineProps<{ target: ObjectRef; payload: ObjectPayload; parsed: ParsedObject | null }>();
const emit = defineEmits<{ openSource: [kind: string, name: string, namespace: string | undefined] }>();

const meta = computed(() => (props.parsed?.metadata ?? {}) as Record<string, unknown>);
const status = computed(() => objectStatus(props.target.gvr, props.parsed));
const fields = computed(() => keyFields(props.target.gvr, props.parsed));
const conditions = computed(() => conditionsOf(props.parsed));
const labels = computed(() => stringMap(meta.value.labels));
const annotations = computed(() => stringMap(meta.value.annotations));
const created = computed(() => {
    const t = Date.parse(String(meta.value.creationTimestamp ?? ''));
    return Number.isFinite(t) ? t : 0;
});
const showLabels = ref(false);
const showAnnotations = ref(false);
const isSecret = computed(() => props.target.gvr === 'v1/secrets');
</script>

<template>
    <div class="ov">
        <div v-if="payload.gitops" class="notice warn" role="note" data-testid="gitops-banner">
            <div>
                <b>Managed by {{ GITOPS_LABEL[payload.gitops.kind] ?? payload.gitops.kind }}.</b>
                {{ payload.gitops.kind }}
                <span class="mono">{{ payload.gitops.namespace ? payload.gitops.namespace + '/' : '' }}{{ payload.gitops.name }}</span>
                owns this object; manual edits may be reverted.
                <div class="row">
                    <button
                        class="btn sm"
                        type="button"
                        @click="emit('openSource', payload.gitops.kind, payload.gitops.name, payload.gitops.namespace)"
                    >
                        Open source
                    </button>
                </div>
            </div>
        </div>

        <dl class="grid">
            <dt class="lbl">Status</dt>
            <dd><span v-if="status" class="tag" :class="status.tone">{{ status.text }}</span><span v-else class="muted">—</span></dd>
            <dt class="lbl">Cluster</dt>
            <dd>
                <span class="tag"><span class="dot" :style="{ background: clusterColor(target.cluster) }"></span>{{ target.cluster }}</span>
            </dd>
            <template v-if="target.namespace"
                ><dt class="lbl">Namespace</dt>
                <dd>{{ target.namespace }}</dd></template
            >
            <dt class="lbl">Created</dt>
            <dd>
                {{ created ? formatAge(created, Date.now()) + ' ago' : '—' }}
                <span v-if="created" class="muted">({{ new Date(created).toLocaleString() }})</span>
            </dd>
            <template v-for="f in fields" :key="f.label"
                ><dt class="lbl">{{ f.label }}</dt>
                <dd :class="{ mono: f.mono }">{{ f.value }}</dd></template
            >
            <dt class="lbl">Owners</dt>
            <dd>
                <template v-if="payload.owners?.length"
                    ><span v-for="o in payload.owners" :key="o.kind + o.name" class="tag own">{{ o.kind }} / {{ o.name }}</span></template
                >
                <span v-else class="muted">none</span>
            </dd>
            <dt class="lbl">UID</dt>
            <dd class="mono">{{ payload.uid }}</dd>
            <dt class="lbl">Version</dt>
            <dd class="mono">{{ payload.resourceVersion }}</dd>
        </dl>

        <section v-if="conditions.length">
            <h3 class="lbl">Conditions</h3>
            <table class="ctable">
                <thead>
                    <tr>
                        <th>Type</th>
                        <th>Status</th>
                        <th>Reason</th>
                        <th>Since</th>
                    </tr>
                </thead>
                <tbody>
                    <tr v-for="c in conditions" :key="c.type">
                        <td>{{ c.type }}</td>
                        <td><span class="tag" :class="conditionTone(c)">{{ c.status }}</span></td>
                        <td :title="c.message">
                            {{ c.reason || '—' }}
                            <div v-if="c.message" class="muted msg">{{ c.message }}</div>
                        </td>
                        <td class="muted">{{ c.lastTransition ? formatAge(Date.parse(c.lastTransition), Date.now()) : '—' }}</td>
                    </tr>
                </tbody>
            </table>
        </section>

        <section>
            <h3 class="lbl">
                <button class="disc" type="button" :aria-expanded="showLabels" @click="showLabels = !showLabels">
                    Labels <span class="muted">{{ labels.length }}</span>
                </button>
            </h3>
            <div v-if="showLabels" class="kv">
                <template v-for="[k, v] in labels" :key="k"><span class="mono k">{{ k }}</span><span class="mono">{{ v }}</span></template>
                <span v-if="!labels.length" class="muted">none</span>
            </div>
        </section>
        <section v-if="!isSecret">
            <h3 class="lbl">
                <button class="disc" type="button" :aria-expanded="showAnnotations" @click="showAnnotations = !showAnnotations">
                    Annotations <span class="muted">{{ annotations.length }}</span>
                </button>
            </h3>
            <div v-if="showAnnotations" class="kv">
                <template v-for="[k, v] in annotations" :key="k"><span class="mono k">{{ k }}</span><span class="mono v">{{ v }}</span></template>
                <span v-if="!annotations.length" class="muted">none</span>
            </div>
        </section>
        <p v-if="isSecret" class="muted note">Secret values are not shown here. Open the YAML tab and reveal them deliberately.</p>
    </div>
</template>

<style scoped>
.ov {
    display: flex;
    flex-direction: column;
    gap: 14px;
}
.grid {
    display: grid;
    grid-template-columns: 96px minmax(0, 1fr);
    gap: 8px 12px;
    margin: 0;
    align-items: center;
}
dd {
    margin: 0;
    word-break: break-all;
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    align-items: center;
}
.own {
    height: auto;
    padding: 2px 6px;
}
h3 {
    margin: 0 0 6px;
}
.disc {
    all: unset;
    cursor: pointer;
    border-radius: 4px;
}
.disc:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
}
.kv {
    display: grid;
    grid-template-columns: minmax(0, 0.8fr) minmax(0, 1.2fr);
    gap: 4px 12px;
    font-size: 12px;
    word-break: break-all;
}
.k {
    color: var(--text2);
}
.ctable {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
}
.ctable th {
    text-align: left;
    font-size: 11px;
    font-weight: 600;
    color: var(--text3);
    padding: 4px 6px;
    border-bottom: 1px solid var(--border);
}
.ctable td {
    padding: 6px;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
}
.msg {
    font-size: 11px;
    word-break: break-word;
}
.row {
    margin-top: 8px;
}
.note {
    font-size: 12px;
    margin: 0;
}
</style>
