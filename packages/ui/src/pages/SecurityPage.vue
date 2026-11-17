<script setup lang="ts">
import type { ImageVulnSummary, ObjectRef } from '@v8s/client';

import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, shallowRef, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import PolicyTable from '@/components/PolicyTable.vue';
import VulnImages from '@/components/VulnImages.vue';
import { kinds } from '@/lib/agent.ts';
import { labelFromGvr } from '@/lib/kinds.ts';
import { defaultPolicyKind, policyKinds } from '@/lib/policy.ts';
import { isPhone } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useIntegrationsStore } from '@/stores/integrations.ts';

// The detail panels (CVE table, policy YAML) load on first use.
const VulnImagePanel = defineAsyncComponent(() => import('@/components/VulnImagePanel.vue'));
const PolicyPanel = defineAsyncComponent(() => import('@/components/PolicyPanel.vue'));

const route = useRoute();
const router = useRouter();
const integrations = useIntegrationsStore();
const clusters = useClustersStore();

const policies = computed(() => route.name === 'security-policies');
const q = (k: string): string => {
    const v = route.query[k];
    return (Array.isArray(v) ? v[0] : v) || '';
};

// ---- Vulnerabilities: ?image=<repo:tag>&source=<trivy|kubescape>[&rg=&rc=&rns=&rn= for an open report] ----
const allImages = shallowRef<readonly ImageVulnSummary[]>([]);
const image = computed(() => q('image'));
const source = computed(() => q('source'));
const summaries = computed(() => allImages.value.filter(i => i.image === image.value && (!source.value || i.source === source.value)));
const selectedKey = computed(() => (image.value ? `${image.value}|${source.value || summaries.value[0]?.source || ''}` : ''));
const report = computed<ObjectRef | null>(() =>
    q('rg') && q('rc') && q('rn') ? { cluster: q('rc'), gvr: q('rg'), namespace: q('rns') || undefined, name: q('rn') } : null
);

function openImage(i: ImageVulnSummary) {
    void router.push({ path: '/security', query: { image: i.image, source: i.source } });
}
function openReport(ref: ObjectRef | null) {
    const base = { image: image.value, source: source.value };
    void router.push({
        path: '/security',
        query: ref ? { ...base, rg: ref.gvr, rc: ref.cluster, rns: ref.namespace ?? '', rn: ref.name ?? '' } : base
    });
}
const closeImage = () => void router.replace({ path: '/security' });

// ---- Policies: ?k=<gvr> selects the kind; ?policy=<gvr>&c=&ns=&n= opens a policy ----
const kindList = computed(() => policyKinds(kinds.value));
const picked = shallowRef('');
const kindGvr = computed(() => {
    const want = q('policy') || q('k') || picked.value;
    return kindList.value.some(k => k.gvr === want) ? want : defaultPolicyKind(kindList.value);
});
const kindInfo = computed(() => kindList.value.find(k => k.gvr === kindGvr.value));
const policy = computed<ObjectRef | null>(() =>
    q('policy') && q('c') && q('n') ? { cluster: q('c'), gvr: q('policy'), namespace: q('ns') || undefined, name: q('n') } : null
);
const kindTitle = (k: { gvr: string }) => kindList.value.find(x => x.gvr === k.gvr)?.kind ?? labelFromGvr(k.gvr);

function pickKind(gvr: string) {
    picked.value = gvr;
    void router.replace({ path: '/security/policies', query: { k: gvr } });
}
const closePolicy = () => void router.replace({ path: '/security/policies', query: { k: kindGvr.value } });

// The policy tables are ordinary ResourceTables, which open a row in the generic drawer (`/r/<gvr>?c=&n=`). Intercept that here and
// open the policy panel instead.
let removeGuard: (() => void) | undefined;
onMounted(() => {
    removeGuard = router.beforeEach((to, from) => {
        if (from.path !== '/security/policies' || !to.path.startsWith('/r/') || !to.query.c || !to.query.n) return true;
        const gvr = to.path.slice(3);
        if (!policyKinds(kinds.value).some(k => k.gvr === gvr)) return true;
        return {
            path: '/security/policies',
            query: { policy: gvr, c: String(to.query.c), ns: to.query.ns ? String(to.query.ns) : '', n: String(to.query.n) },
            replace: !!from.query.policy
        };
    });
});
onBeforeUnmount(() => removeGuard?.());
watch(
    () => route.name,
    () => (picked.value = '')
);

const panelOpen = computed(() => (policies.value ? !!policy.value : !!image.value));
</script>

<template>
    <main class="page" :class="{ split: panelOpen && !isPhone, overlay: panelOpen && isPhone }">
        <template v-if="!policies">
            <div class="main">
                <VulnImages :selected="selectedKey" @open="openImage" @images="allImages = $event" />
            </div>
            <VulnImagePanel
                v-if="image"
                :key="image + source"
                class="detail"
                :image="image"
                :source="source"
                :summaries="[...summaries]"
                :report="report"
                @open-report="openReport"
                @close="closeImage"
            />
        </template>
        <template v-else>
            <div class="main" data-testid="policies-page">
                <div class="head">
                    <h1>Policies</h1>
                    <span v-if="!integrations.detected.kyverno" class="muted"
                        >Kyverno was not detected in the selected clusters ({{ clusters.activeIds.length }}).</span
                    >
                </div>
                <div v-if="kindList.length" class="seg" role="tablist" aria-label="Policy kind">
                    <button
                        v-for="k in kindList"
                        :key="k.gvr"
                        class="segbtn"
                        :class="{ on: k.gvr === kindGvr }"
                        type="button"
                        role="tab"
                        :aria-selected="k.gvr === kindGvr"
                        @click="pickKind(k.gvr)"
                    >
                        {{ kindTitle(k) }}
                    </button>
                </div>
                <p v-else class="muted" role="status">No Kyverno policy kinds are served by the selected clusters.</p>
                <PolicyTable v-if="kindInfo" :key="kindInfo.gvr" :gvr="kindInfo.gvr" :title="kindTitle(kindInfo)" :namespaced="kindInfo.namespaced" />
            </div>
            <PolicyPanel
                v-if="policy"
                :key="`${policy.cluster}|${policy.gvr}|${policy.namespace}|${policy.name}`"
                class="detail"
                :target="policy"
                @close="closePolicy"
            />
        </template>
    </main>
</template>

<style scoped>
.page {
    flex: 1 1 0;
    min-width: 0;
    min-height: 0;
    display: flex;
    gap: 12px;
    padding: 16px 20px 12px;
}
.main {
    flex: 1 1 0;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
}
.detail {
    flex: none;
    width: min(560px, 46vw);
    min-height: 0;
}
.head {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
}
h1 {
    font-size: 19px;
    font-weight: 650;
    letter-spacing: -0.01em;
    margin: 0;
}
.seg {
    display: flex;
    gap: 4px;
    overflow-x: auto;
    flex: none;
    padding-bottom: 2px;
}
.segbtn {
    all: unset;
    box-sizing: border-box;
    flex: none;
    height: 30px;
    padding: 0 12px;
    display: inline-flex;
    align-items: center;
    border: 1px solid var(--bstrong);
    border-radius: 8px;
    background: var(--surface);
    font-size: 13px;
    font-weight: 500;
    color: var(--text2);
    cursor: pointer;
    white-space: nowrap;
}
.segbtn:hover {
    color: var(--text);
}
.segbtn.on {
    background: var(--accent-soft);
    border-color: var(--accent);
    color: var(--text);
}
.segbtn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 1px;
}
@media (max-width: 639.98px) {
    .page {
        padding: 12px 12px 8px;
    }
    .overlay .main {
        display: none;
    }
    .overlay .detail {
        width: 100%;
        flex: 1 1 0;
    }
}
@media (pointer: coarse) {
    .segbtn {
        height: 44px;
    }
}
</style>
