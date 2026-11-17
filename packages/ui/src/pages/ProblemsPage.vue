<script setup lang="ts">
import { ProblemsGVR } from '@v8s/client';
import { computed, onBeforeUnmount, onMounted } from 'vue';

import type { GroupBy } from '@/lib/problems.ts';

import NamespaceMenu from '@/components/NamespaceMenu.vue';
import ProblemsChips from '@/components/ProblemsChips.vue';
import ProblemsList from '@/components/ProblemsList.vue';
import ProblemsPresets from '@/components/ProblemsPresets.vue';
import { formatCount } from '@/lib/format.ts';
import { errorText } from '@/lib/mutations.ts';
import { useProblemsView } from '@/lib/problems-view.ts';
import { AGGREGATE_CAP, classPatch, familyChips, SEVERITY_FILTERS } from '@/lib/problems.ts';
import { isPhone } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useProblemsStore } from '@/stores/problems.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useTableStore } from '@/stores/table.ts';
import { useUiStore } from '@/stores/ui.ts';

const store = useProblemsStore();
const clusters = useClustersStore();
const settings = useSettingsStore();
const table = useTableStore();
const ui = useUiStore();
const view = useProblemsView();

onMounted(() => (table.currentGvr = ProblemsGVR));
onBeforeUnmount(() => {
    if (table.currentGvr === ProblemsGVR) table.currentGvr = '';
});

const chips = computed(() => familyChips(view.classes.value?.classes ?? [], clusters.activeIds, store.showAcked));
const summary = computed(() => {
    const n = clusters.activeIds.length;
    const t = view.totals.value;
    const scope = isPhone.value ? '' : ` across ${n} cluster${n === 1 ? '' : 's'}`;
    const hidden = t.acked && !store.showAcked ? ` · ${formatCount(t.acked)} acknowledged${isPhone.value ? '' : ' hidden'}` : '';
    return `${formatCount(t.shown)}${scope}${hidden}`;
});

async function setClass(cls: string, on: boolean) {
    try {
        await settings.patch(classPatch({ [cls]: on }));
    } catch (e) {
        ui.toast('danger', `Could not change ${cls}: ${errorText(e)}`);
    }
    await view.reloadClasses();
}

const groupOptions: { id: GroupBy; label: string }[] = [
    { id: 'none', label: 'No grouping' },
    { id: 'class', label: 'Class' },
    { id: 'cluster', label: 'Cluster' },
    { id: 'namespace', label: 'Namespace' }
];
const groupDisabled = computed(() => view.aggregateActive.value);
</script>

<template>
    <main class="page">
        <div class="head">
            <h1>Problems</h1>
            <span class="muted summary" aria-live="polite" data-testid="problems-summary">{{ summary }}</span>
            <div class="grow"></div>
            <div class="seg" role="group" aria-label="Severity">
                <button
                    v-for="s in SEVERITY_FILTERS"
                    :key="s.id"
                    type="button"
                    :class="{ on: store.severity === s.id }"
                    :aria-pressed="store.severity === s.id"
                    :data-testid="`sev-${s.id}`"
                    @click="store.severity = s.id"
                >
                    {{ s.label }}
                </button>
            </div>
            <ProblemsPresets :classes="view.classes.value?.classes ?? []" @changed="view.reloadClasses()" />
            <label class="sel opt">
                <span class="sr-only">Group by</span>
                <select v-model="store.groupBy" class="btn" :disabled="groupDisabled" aria-label="Group by" data-testid="group-by">
                    <option v-for="g in groupOptions" :key="g.id" :value="g.id" :disabled="g.id === 'namespace' && view.namespaceTooMany.value">
                        {{ g.id === 'none' ? g.label : 'Group: ' + g.label.toLowerCase() }}
                    </option>
                </select>
            </label>
            <label class="sel opt">
                <span class="sr-only">Order</span>
                <select v-model="store.oldest" class="btn" aria-label="Order" :disabled="view.aggregateActive.value">
                    <option :value="false">Newest change first</option>
                    <option :value="true">Oldest first</option>
                </select>
            </label>
        </div>
        <div class="filters">
            <label class="field filter">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                    <circle cx="7" cy="7" r="4.5" />
                    <path d="M10.5 10.5L14 14" />
                </svg>
                <input
                    id="pods-filter"
                    v-model="store.text"
                    type="search"
                    placeholder="Search by object name"
                    aria-label="Search problems by object name"
                    autocomplete="off"
                    spellcheck="false"
                />
                <span class="kbd">/</span>
            </label>
            <div class="chips" role="group" aria-label="Problem families">
                <ProblemsChips :chips="chips" :selected="store.families" @toggle="store.toggleFamily" @set-class="setClass" />
            </div>
            <div class="grow"></div>
            <label
                class="chk opt"
                :title="
                    view.aggregateBlocked.value
                        ? `Needs ${formatCount(AGGREGATE_CAP)} problems or fewer`
                        : 'Collapse the same problem across clusters'
                "
            >
                <input
                    v-model="store.aggregate"
                    class="cb"
                    type="checkbox"
                    :disabled="clusters.activeIds.length < 2 || view.aggregateBlocked.value"
                    data-testid="aggregate"
                />
                Across clusters
            </label>
            <label class="chk">
                <input v-model="store.showAcked" class="cb" type="checkbox" data-testid="show-acked" />
                Show acknowledged
            </label>
        </div>
        <div v-if="view.aggregateBlocked.value" class="notice warn" role="status">
            Cross-cluster aggregation is limited to {{ formatCount(AGGREGATE_CAP) }} problems; narrow the filters to use it.
        </div>
        <div v-if="store.groupBy === 'namespace' && view.namespaceTooMany.value" class="notice warn" role="status">
            Too many namespaces to group by (more than 100); choose a namespace filter first.
        </div>
        <div v-for="u in view.unavailable.value" :key="u.cluster" class="notice warn" role="status" data-testid="problems-unavailable">
            <span class="grow">{{ u.text }}</span>
            <button class="btn sm ghost" type="button" :aria-label="`Dismiss: ${u.text}`" @click="store.dismiss(u.keys)">Dismiss</button>
        </div>
        <div v-if="view.classesError.value" class="notice danger" role="alert">Could not load problem classes: {{ view.classesError.value }}</div>
        <div class="card table-card">
            <ProblemsList :view="view" />
        </div>
    </main>
</template>

<style scoped>
.page {
    flex: 1 1 0;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    padding: 16px 20px 12px;
    gap: 12px;
}
.head,
.filters {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.head {
    gap: 12px;
}
h1 {
    font-size: 19px;
    font-weight: 650;
    letter-spacing: -0.01em;
}
.summary {
    font-size: 13px;
}
.filter {
    width: 300px;
    max-width: 100%;
}
.chips {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    align-items: center;
}
.chk {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: 13px;
    color: var(--text2);
    cursor: pointer;
}
.seg {
    display: inline-flex;
    box-shadow: var(--rest);
    border-radius: 8px;
}
.seg button {
    height: 32px;
    padding: 0 10px;
    border: 1px solid var(--bstrong);
    margin-left: -1px;
    background: var(--surface);
    color: var(--text2);
    font-family: inherit;
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
}
.seg button:first-child {
    border-radius: 8px 0 0 8px;
    margin-left: 0;
}
.seg button:last-child {
    border-radius: 0 8px 8px 0;
}
.seg button.on {
    background: var(--s3);
    color: var(--text);
}
.sel select {
    padding-right: 8px;
}
@media (max-width: 899.98px) {
    .opt {
        display: none;
    }
}
@media (max-width: 639.98px) {
    .page {
        padding: 12px 0 0;
        gap: 10px;
    }
    .head,
    .filters {
        padding: 0 14px;
    }
    .head {
        gap: 8px;
    }
    .grow {
        display: none;
    }
    .seg {
        width: 100%;
    }
    .seg button {
        flex: 1;
        height: 40px;
    }
    .filter {
        width: 100%;
        height: 40px;
    }
    .chips {
        flex-wrap: nowrap;
        overflow-x: auto;
        width: 100%;
        padding-bottom: 2px;
    }
    .notice {
        margin: 0 14px;
    }
    .table-card {
        border-radius: 0;
        border-left: 0;
        border-right: 0;
    }
}
</style>
