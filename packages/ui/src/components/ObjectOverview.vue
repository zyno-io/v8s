<script setup lang="ts">
import type { ObjectPayload, ObjectRef } from '@v8s/client';

import { computed, ref, shallowRef, watch } from 'vue';

import type { ParsedObject } from '@/lib/object-session.ts';

import ProtectedMark from '@/components/ProtectedMark.vue';
import VpaRecommendations from '@/components/VpaRecommendations.vue';
import { getAgent, kinds } from '@/lib/agent.ts';
import { clusterColor } from '@/lib/colors.ts';
import { formatAge } from '@/lib/format.ts';
import { resolveKind } from '@/lib/kinds.ts';
import { errorText } from '@/lib/mutations.ts';
import { conditionsOf, conditionTone, GITOPS_LABEL, keyFields, objectStatus, stringMap } from '@/lib/overview.ts';
import { isVpaGvr, podRequests, VPA_GVR, vpaNameFromAnnotation, vpaRecommendations } from '@/lib/vpa.ts';
import { useClustersStore } from '@/stores/clusters.ts';

const clusters = useClustersStore();
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
const showLabels = ref(true);
const showAnnotations = ref(true);
// Long annotation values (last-applied-configuration, JSON blobs) are clamped until expanded one by one.
const CLAMP_CHARS = 240;
const CLAMP_LINES = 4;
const expanded = ref(new Set<string>());
const isLong = (v: string) => v.length > CLAMP_CHARS || v.split('\n').length > CLAMP_LINES;
function toggleValue(k: string) {
    const next = new Set(expanded.value);
    if (!next.delete(k)) next.add(k);
    expanded.value = next;
}

// ---- Vertical Pod Autoscaler ----
const isVpa = computed(() => isVpaGvr(props.target.gvr));
const ownRecs = computed(() => (isVpa.value ? vpaRecommendations(props.parsed) : []));
const vpaUpdates = computed(() =>
    props.target.gvr === 'v1/pods'
        ? vpaNameFromAnnotation((meta.value.annotations as Record<string, unknown> | undefined)?.vpaUpdates as string | undefined)
        : null
);
const requests = computed(() => (vpaUpdates.value ? podRequests(props.parsed) : []));
const vpaObj = shallowRef<Record<string, unknown> | null>(null);
const vpaMsg = ref('');
const vpaRecs = computed(() => vpaRecommendations(vpaObj.value));
// Fetched once per pod open (and again only when the VPA's name changes), not on every pod resourceVersion bump.
watch(
    () => (vpaUpdates.value ? `${props.target.cluster}|${props.target.namespace}|${vpaUpdates.value.name}` : ''),
    async key => {
        vpaObj.value = null;
        vpaMsg.value = '';
        if (!key) return;
        const name = vpaUpdates.value!.name;
        const gvr = resolveKind(kinds.value, 'VerticalPodAutoscaler', 'autoscaling.k8s.io')?.gvr ?? VPA_GVR;
        vpaMsg.value = 'Loading the recommendation…';
        try {
            const p = await getAgent().call<ObjectPayload>('object.get', {
                cluster: props.target.cluster,
                gvr,
                namespace: props.target.namespace,
                name
            });
            if (key !== `${props.target.cluster}|${props.target.namespace}|${vpaUpdates.value?.name}`) return;
            const { load } = await import('js-yaml');
            const doc = load(p.yaml);
            vpaObj.value = doc && typeof doc === 'object' ? (doc as Record<string, unknown>) : null;
            vpaMsg.value = '';
        } catch (e) {
            if (key === `${props.target.cluster}|${props.target.namespace}|${vpaUpdates.value?.name}`)
                vpaMsg.value = `Could not read VPA ${name}: ${errorText(e)}`;
        }
    },
    { immediate: true }
);
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
                <span class="tag"
                    ><span class="dot" :style="{ background: clusterColor(target.cluster) }"></span>{{ clusters.displayName(target.cluster)
                    }}<ProtectedMark :cluster="target.cluster"
                /></span>
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

        <section v-if="isVpa" data-testid="vpa-section">
            <h3 class="lbl">Recommendations</h3>
            <VpaRecommendations v-if="ownRecs.length" :recommendations="ownRecs" />
            <p v-else class="muted none">No recommendation yet</p>
        </section>

        <section v-if="vpaUpdates" data-testid="vpa-pod">
            <h3 class="lbl">Vertical Pod Autoscaler</h3>
            <p class="vpa">
                Requests set by VPA <b>{{ vpaUpdates.name }}</b>
                <button class="btn sm" type="button" @click="emit('openSource', 'VerticalPodAutoscaler', vpaUpdates.name, target.namespace)">
                    Open VPA
                </button>
            </p>
            <p v-if="vpaUpdates.updated" class="muted none">{{ vpaUpdates.updated }}</p>
            <VpaRecommendations v-if="vpaRecs.length" :recommendations="vpaRecs" :requests="requests" />
            <p v-else-if="vpaMsg" class="muted none">{{ vpaMsg }}</p>
            <p v-else-if="vpaObj" class="muted none">No recommendation yet</p>
        </section>

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
                    <svg class="chev" :class="{ open: showLabels }" width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
                        <path d="M3.5 2l3 3-3 3" fill="none" stroke="currentColor" stroke-width="1.5" />
                    </svg>
                    Labels <span class="count">{{ labels.length }}</span>
                </button>
            </h3>
            <template v-if="showLabels">
                <ul v-if="labels.length" class="chips" data-testid="labels">
                    <li v-for="[k, v] in labels" :key="k" class="chip mono" :title="`${k}=${v}`">
                        <span class="ck">{{ k }}</span><template v-if="v"><span class="eq">=</span><span class="cv">{{ v }}</span></template>
                    </li>
                </ul>
                <p v-else class="muted none">none</p>
            </template>
        </section>
        <section v-if="!isSecret">
            <h3 class="lbl">
                <button class="disc" type="button" :aria-expanded="showAnnotations" @click="showAnnotations = !showAnnotations">
                    <svg class="chev" :class="{ open: showAnnotations }" width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
                        <path d="M3.5 2l3 3-3 3" fill="none" stroke="currentColor" stroke-width="1.5" />
                    </svg>
                    Annotations <span class="count">{{ annotations.length }}</span>
                </button>
            </h3>
            <template v-if="showAnnotations">
                <ul v-if="annotations.length" class="annos" data-testid="annotations">
                    <li v-for="[k, v] in annotations" :key="k">
                        <div class="ak mono">{{ k }}</div>
                        <div class="av mono" :class="{ clamp: isLong(v) && !expanded.has(k), empty: !v }">{{ v || 'empty' }}</div>
                        <button v-if="isLong(v)" class="more" type="button" :aria-expanded="expanded.has(k)" @click="toggleValue(k)">
                            {{ expanded.has(k) ? 'Show less' : `Show all (${v.length.toLocaleString('en-US')} chars)` }}
                        </button>
                    </li>
                </ul>
                <p v-else class="muted none">none</p>
            </template>
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
    display: inline-flex;
    align-items: center;
    gap: 5px;
}
.chev {
    color: var(--text3);
    transition: transform 0.12s ease;
}
.chev.open {
    transform: rotate(90deg);
}
.count {
    font-weight: 500;
    color: var(--text3);
    background: var(--s2);
    border-radius: 8px;
    padding: 0 6px;
    font-size: 10.5px;
    line-height: 16px;
}
.none {
    margin: 0;
    font-size: 12px;
}
/* Labels: one chip per label, so a wrapped key or value never reads as the next label. */
.chips {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
}
.chip {
    display: inline-flex;
    max-width: 100%;
    padding: 2px 7px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--s2);
    font-size: 11.5px;
    line-height: 17px;
    overflow-wrap: anywhere;
}
.ck {
    color: var(--text2);
}
.eq {
    color: var(--text3);
    padding: 0 1px;
}
.cv {
    color: var(--text);
}
/* Annotations: key over value, one bordered block per entry, long values clamped. */
.annos {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
}
.annos li {
    padding: 7px 10px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
}
.annos li + li {
    border-top: 1px solid var(--border);
}
.ak {
    font-size: 11px;
    color: var(--text2);
    overflow-wrap: anywhere;
}
.av {
    font-size: 12px;
    color: var(--text);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
}
.av.clamp {
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 4;
    line-clamp: 4;
    overflow: hidden;
}
.av.empty {
    color: var(--text3);
    font-style: italic;
}
.more {
    all: unset;
    cursor: pointer;
    align-self: flex-start;
    font-size: 11px;
    color: var(--link);
    border-radius: 3px;
}
.more:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
}
.disc:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
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
.vpa {
    margin: 0 0 4px;
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
}
.note {
    font-size: 12px;
    margin: 0;
}
</style>
