<script setup lang="ts">
import type { ObjectPayload, ObjectRef } from '@v8s/client';

import { computed, onBeforeUnmount } from 'vue';
import { useRouter } from 'vue-router';

import { getAgent, kinds } from '@/lib/agent.ts';
import { openObject } from '@/lib/drawer.ts';
import { ownerTarget } from '@/lib/row-keys.ts';

const props = defineProps<{ target: ObjectRef; payload: ObjectPayload }>();
const router = useRouter();

const owners = computed(() => (props.payload.owners ?? []).map(o => ({ ...o, to: ownerTarget(props.target, o, kinds.value) })));

// Owned objects, for the two parent kinds whose children expose an `owner` column.
const OWNED: Record<string, { gvr: string; label: string }> = {
    'apps/v1/deployments': { gvr: 'apps/v1/replicasets', label: 'ReplicaSets' },
    'batch/v1/cronjobs': { gvr: 'batch/v1/jobs', label: 'Jobs' }
};
const owned = OWNED[props.target.gvr];
const handle = owned
    ? getAgent().subscribeView(
          {
              clusters: [props.target.cluster],
              gvr: owned.gvr,
              namespaces: props.target.namespace ? [props.target.namespace] : [],
              text: props.target.name ?? '',
              filters: [{ col: 'owner', op: 'eq', value: props.target.name ?? '' }],
              sort: []
          },
          { offset: 0, limit: 50 }
      )
    : null;
onBeforeUnmount(() => handle?.close());

const ownedRows = computed(() => {
    if (!handle) return [];
    void handle.version.value;
    const cols = handle.columns.value ?? [];
    const ni = cols.findIndex(c => c.key === 'name') + 2;
    const si = cols.findIndex(c => c.key === 'statusTone') + 2;
    return [...handle.ids]
        .map(id => handle.rows.get(id))
        .filter(Boolean)
        .map(r => ({ name: String(r![ni]), tone: si >= 2 ? Number(r![si]) : 0 }));
});
const TONES = ['neutral', 'success', 'warning', 'danger', 'info', 'release'];
function openOwned(name: string) {
    if (!owned) return;
    void openObject(router, { cluster: props.target.cluster, gvr: owned.gvr, namespace: props.target.namespace, name });
}
</script>

<template>
    <div class="rel">
        <section>
            <h3 class="lbl">Owned by</h3>
            <p v-if="!owners.length" class="muted">No owners.</p>
            <ul v-else class="list">
                <li v-for="o in owners" :key="o.kind + o.name">
                    <span class="tag">{{ o.kind }}</span>
                    <span class="mono">{{ o.name }}</span>
                    <button v-if="o.to" class="btn sm" type="button" @click="openObject(router, o.to)">Open</button>
                </li>
            </ul>
        </section>
        <section v-if="owned">
            <h3 class="lbl">Owns · {{ owned.label }}</h3>
            <p v-if="!ownedRows.length" class="muted">None.</p>
            <ul v-else class="list">
                <li v-for="r in ownedRows" :key="r.name">
                    <span class="mono">{{ r.name }}</span>
                    <span class="tag" :class="TONES[r.tone]">{{ TONES[r.tone] }}</span>
                    <button class="btn sm" type="button" @click="openOwned(r.name)">Open</button>
                </li>
            </ul>
        </section>
    </div>
</template>

<style scoped>
.rel {
    display: flex;
    flex-direction: column;
    gap: 16px;
}
h3 {
    margin: 0 0 6px;
}
.list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
}
li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 8px;
    border: 1px solid var(--border);
    border-radius: 8px;
    word-break: break-all;
}
li .btn {
    margin-left: auto;
}
</style>
