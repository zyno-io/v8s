<script setup lang="ts">
import type { ObjectRef, ViewHandle } from '@v8s/client';

import { MaxObjectEvents } from '@v8s/client';
import { computed, onBeforeUnmount, shallowRef } from 'vue';

import { getAgent } from '@/lib/agent.ts';
import { eventRows, eventsErrorText, eventsRetryable, eventsState, eventTone } from '@/lib/events.ts';
import { formatAge } from '@/lib/format.ts';

const props = defineProps<{ target: ObjectRef }>();

// Every event of the object (Normal and Warning), listed by the agent on demand while this tab is open. The ref is
// copied into a plain object: props are reactive proxies, which cannot be posted to the worker.
function open(): ViewHandle {
    const t = props.target;
    return getAgent().subscribeView(
        {
            clusters: [t.cluster],
            gvr: 'v1/events',
            namespaces: [],
            text: '',
            filters: [],
            sort: [{ col: 'lastSeen', desc: true }],
            involved: { cluster: t.cluster, gvr: t.gvr, namespace: t.namespace, name: t.name }
        },
        { offset: 0, limit: MaxObjectEvents }
    );
}

const handle = shallowRef<ViewHandle>(open());
onBeforeUnmount(() => handle.value.close());

function retry() {
    handle.value.close();
    handle.value = open();
}

const rows = computed(() => eventRows(handle.value));
const state = computed(() => eventsState(handle.value, rows.value.length));
const error = computed(() => handle.value.error.value);
</script>

<template>
    <div class="ev" data-testid="events-pane">
        <p v-if="state === 'loading'" class="muted" role="status">Loading events…</p>
        <div v-else-if="state === 'error' && error" class="notice danger" role="alert">
            {{ eventsErrorText(error, target) }}
            <button v-if="eventsRetryable(error)" class="btn sm" type="button" @click="retry">Retry</button>
        </div>
        <p v-else-if="state === 'empty'" class="muted" role="status">No events for this object.</p>
        <template v-else>
            <ul class="list">
                <li v-for="r in rows" :key="r.id" :data-type="r.type">
                    <div class="top">
                        <span class="tag" :class="eventTone(r.type)">{{ r.type }}</span>
                        <b>{{ r.reason }}</b>
                        <span v-if="r.count > 1" class="muted">×{{ r.count }}</span>
                        <span class="grow"></span>
                        <span v-if="r.source" class="muted">from {{ r.source }}</span>
                        <span class="muted">{{ formatAge(r.lastSeen, Date.now()) }} ago</span>
                    </div>
                    <div class="msg">{{ r.message }}</div>
                </li>
            </ul>
            <p v-if="rows.length >= MaxObjectEvents" class="muted note">Showing the newest {{ MaxObjectEvents }} events.</p>
        </template>
    </div>
</template>

<style scoped>
.note {
    margin: 8px 0 0;
    font-size: 12px;
}
.notice .btn {
    margin-left: 8px;
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
