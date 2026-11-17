<script setup lang="ts">
import type { ObjectRef } from '@v8s/client';

import { computed, onBeforeUnmount } from 'vue';

import { getAgent, kinds } from '@/lib/agent.ts';
import { formatAge } from '@/lib/format.ts';

const props = defineProps<{ target: ObjectRef }>();

const kindName = computed(() => kinds.value.find(k => k.gvr === props.target.gvr)?.kind ?? '');

// The events view's `object` column is "Kind/name"; a `contains` filter narrows it to this object, scoped to its namespace and cluster.
const handle = getAgent().subscribeView(
    {
        clusters: [props.target.cluster],
        gvr: 'v1/events',
        namespaces: props.target.namespace ? [props.target.namespace] : [],
        text: '',
        filters: [{ col: 'object', op: 'contains', value: `${kindName.value ? kindName.value + '/' : ''}${props.target.name}` }],
        sort: [{ col: 'lastSeen', desc: true }]
    },
    { offset: 0, limit: 50 }
);
onBeforeUnmount(() => handle.close());

const idx = computed(() => {
    const m: Record<string, number> = {};
    (handle.columns.value ?? []).forEach((c, i) => (m[c.key] = i + 2));
    return m;
});
const rows = computed(() => {
    void handle.version.value;
    return [...handle.ids].map(id => handle.rows.get(id)).filter((r): r is NonNullable<typeof r> => !!r);
});
const supported = computed(() => handle.columns.value === null || handle.columns.value.length > 0);
</script>

<template>
    <div class="ev">
        <p class="muted note">The agent keeps Warning events only, so normal events do not appear here.</p>
        <p v-if="!supported" class="muted">No selected cluster serves events.</p>
        <p v-else-if="!rows.length" class="muted" role="status">No warning events for this object.</p>
        <ul v-else class="list">
            <li v-for="r in rows" :key="r[0]">
                <div class="top">
                    <span class="tag warning">{{ r[idx.reason] }}</span>
                    <span v-if="Number(r[idx.count]) > 1" class="muted">×{{ r[idx.count] }}</span>
                    <span class="grow"></span>
                    <span class="muted">{{ formatAge(Number(r[idx.lastSeen]), Date.now()) }} ago</span>
                </div>
                <div class="msg">{{ r[idx.message] }}</div>
            </li>
        </ul>
    </div>
</template>

<style scoped>
.note {
    margin: 0 0 8px;
    font-size: 12px;
}
.list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
}
li {
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: 8px;
}
.top {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
}
.msg {
    margin-top: 4px;
    font-size: 13px;
    word-break: break-word;
}
</style>
