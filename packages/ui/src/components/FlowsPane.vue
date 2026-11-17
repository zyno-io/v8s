<script setup lang="ts">
import type { FlowStreamHandle, ObjectRef } from '@v8s/client';

import { computed, onBeforeUnmount, reactive, ref, shallowRef, watch } from 'vue';
import { useRoute } from 'vue-router';

import { getAgent } from '@/lib/agent.ts';
import {
    emptyFilter,
    FILTER_DEBOUNCE_MS,
    filterKey,
    flowCapacity,
    flowScope,
    noRelay,
    PROTOCOLS,
    rateText,
    relevantInputs,
    reopenDecision,
    toFlowFilter,
    verdictFromQuery,
    VERDICTS
} from '@/lib/flows.ts';
import { errorText } from '@/lib/mutations.ts';
import { isPhone } from '@/lib/viewport.ts';

import FlowTable from './FlowTable.vue';

const props = defineProps<{ target: ObjectRef; parsed: Record<string, unknown> | null }>();
const route = useRoute();

const filter = reactive({ ...emptyFilter(), verdict: verdictFromQuery(route.query.fv) });
const applied = ref({ ...filter });
const follow = ref(true);
const reconnects = ref(0);

const scope = computed(() => flowScope(props.target));
const rel = computed(() => relevantInputs(filter));

// ---- stream lifecycle ----
const handle = shallowRef<FlowStreamHandle | null>(null);
const opening = ref(false);
const unavailable = ref<{ message: string; hint: string } | null>(null);
const openError = ref('');
let token = 0;

function closeHandle() {
    handle.value?.close();
    handle.value = null;
}

async function open() {
    const my = ++token;
    closeHandle();
    unavailable.value = null;
    openError.value = '';
    const sc = scope.value;
    if (!sc) {
        openError.value = 'Flows are not available for this object.';
        return;
    }
    opening.value = true;
    try {
        const h = await getAgent().openStream('flows', {
            cluster: props.target.cluster,
            scope: sc,
            filter: toFlowFilter(applied.value),
            capacity: flowCapacity(isPhone.value)
        });
        if (my !== token) {
            h.close();
            return;
        }
        handle.value = h;
        follow.value = true;
    } catch (e) {
        if (my !== token) return;
        unavailable.value = noRelay(e);
        if (!unavailable.value) openError.value = errorText(e);
    } finally {
        if (my === token) opening.value = false;
    }
}

// Scope changes and reconnects reopen at once; filters reopen after a pause in typing (they are applied by the relay).
const openKey = computed(
    () => `${props.target.cluster}|${props.target.gvr}|${props.target.namespace ?? ''}|${props.target.name ?? ''}|${reconnects.value}`
);
watch(openKey, () => void open(), { immediate: true });

let timer: ReturnType<typeof setTimeout> | undefined;
function applyNow() {
    clearTimeout(timer);
    timer = undefined;
    applied.value = { ...filter };
    void open();
}
watch(
    () => ({ ...filter }),
    next => {
        const d = reopenDecision(applied.value, next);
        if (d === 'none') {
            applied.value = next;
            return;
        }
        clearTimeout(timer);
        if (d === 'now') applyNow();
        else timer = setTimeout(applyNow, FILTER_DEBOUNCE_MS);
    }
);
onBeforeUnmount(() => {
    token++;
    clearTimeout(timer);
    closeHandle();
});

const state = computed(() => handle.value?.state.value);
const rate = computed(() => rateText(handle.value?.rate.value));
const sampled = computed(() => handle.value?.rate.value?.sampled === true);
const clear = () => handle.value?.clear();
const reconnect = () => reconnects.value++;
const statusText = computed(() => {
    if (opening.value) return 'Connecting';
    if (openError.value) return 'Error';
    if (state.value === 'live') return 'Live';
    if (state.value === 'ended') return 'Ended';
    if (state.value === 'error') return 'Error';
    return '';
});
const toneClass = computed(() => (openError.value || state.value === 'error' ? 'danger' : state.value === 'live' ? 'success' : ''));
const filterActive = computed(() => filterKey(applied.value) !== '{}');
</script>

<template>
    <div class="flows" data-testid="flows-pane">
        <div class="bar" role="toolbar" aria-label="Flow filters">
            <label class="sel">
                <span class="sr-only">Verdict</span>
                <select v-model="filter.verdict" aria-label="Verdict" data-testid="flows-verdict">
                    <option value="">All verdicts</option>
                    <option v-for="v in VERDICTS" :key="v" :value="v">{{ v.charAt(0) + v.slice(1).toLowerCase() }}</option>
                </select>
            </label>
            <label class="sel">
                <span class="sr-only">Direction</span>
                <select v-model="filter.direction" aria-label="Direction" data-testid="flows-direction">
                    <option value="">Both directions</option>
                    <option value="INGRESS">Ingress</option>
                    <option value="EGRESS">Egress</option>
                </select>
            </label>
            <label class="sel">
                <span class="sr-only">Protocol</span>
                <select v-model="filter.protocol" aria-label="Protocol" data-testid="flows-protocol">
                    <option value="">All protocols</option>
                    <option v-for="p in PROTOCOLS" :key="p" :value="p">{{ p.toUpperCase() }}</option>
                </select>
            </label>
            <div v-if="rel.dropReason" class="field fld">
                <input v-model="filter.dropReason" type="search" placeholder="Drop reason" aria-label="Drop reason" data-testid="flows-reason" />
            </div>
            <div v-if="rel.httpStatus" class="field fld narrow">
                <input v-model="filter.httpStatus" type="search" placeholder="HTTP status (5+)" aria-label="HTTP status" data-testid="flows-status" />
            </div>
            <div v-if="rel.dnsQuery" class="field fld">
                <input v-model="filter.dnsQuery" type="search" placeholder="DNS query" aria-label="DNS query" data-testid="flows-dns" />
            </div>
            <span class="grow"></span>
            <button type="button" class="btn sm tog" :aria-pressed="follow" data-testid="flows-follow" @click="follow = !follow">
                {{ follow ? 'Pause' : 'Follow' }}
            </button>
            <button type="button" class="btn sm" :disabled="!handle" data-testid="flows-clear" @click="clear">Clear</button>
        </div>

        <div class="status" role="status" aria-live="polite">
            <span v-if="statusText" class="tag" :class="toneClass">{{ statusText }}</span>
            <span v-if="rate" class="rate" :class="{ warn: sampled }" data-testid="flows-rate">{{ rate }}</span>
            <span v-if="state === 'error' && handle?.endError.value" class="msg">{{ handle.endError.value }}</span>
            <button v-if="state === 'ended' || state === 'error'" type="button" class="btn sm" data-testid="flows-reconnect" @click="reconnect">
                Reconnect
            </button>
        </div>

        <div v-if="unavailable" class="notice" role="alert" data-testid="flows-unavailable">
            <span
                ><strong>No Hubble relay in this cluster.</strong> {{ unavailable.message
                }}<template v-if="unavailable.hint"> {{ unavailable.hint }}</template></span
            >
            <button type="button" class="btn sm" @click="reconnect">Retry</button>
        </div>
        <p v-else-if="openError" class="notice danger" role="alert">
            <span>{{ openError }}</span>
            <button type="button" class="btn sm" @click="reconnect">Reconnect</button>
        </p>
        <p v-else-if="opening && !handle" class="muted" role="status">Connecting to Hubble…</p>

        <FlowTable v-if="handle" :handle="handle" :follow="follow" @update:follow="follow = $event" />
        <p v-if="handle && !handle.total.value && state === 'live'" class="muted empty">
            Waiting for flows<template v-if="filterActive"> matching the filter</template>…
        </p>
    </div>
</template>

<style scoped>
.flows {
    display: flex;
    flex-direction: column;
    gap: 8px;
    height: calc(100dvh - 230px);
    min-height: 320px;
}
.bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
}
.grow {
    flex: 1;
}
.sel select {
    height: 26px;
    border: 1px solid var(--bstrong);
    border-radius: 5px;
    background: var(--surface);
    color: var(--text);
    font-size: 12px;
    padding: 0 6px;
    max-width: 170px;
}
.fld {
    height: 26px;
    border-radius: 5px;
    min-width: 120px;
    flex: 0 1 170px;
}
.fld.narrow {
    flex-basis: 140px;
}
.fld input {
    font-size: 12px;
}
.tog[aria-pressed='false'] {
    border-color: var(--focus);
    background: var(--accent-soft);
}
.status {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    min-height: 22px;
    font-size: 12px;
}
.rate {
    color: var(--text2);
}
.rate.warn {
    color: var(--warning);
}
.msg {
    color: var(--danger);
}
.notice {
    justify-content: space-between;
    align-items: center;
}
.empty {
    margin: 0;
    font-size: 12px;
}
@media (pointer: coarse) {
    .btn.sm,
    .sel select,
    .fld {
        min-height: 44px;
    }
    .bar {
        gap: 8px;
    }
}
@media (max-width: 639.98px) {
    .flows {
        height: calc(100dvh - 190px);
    }
}
</style>
