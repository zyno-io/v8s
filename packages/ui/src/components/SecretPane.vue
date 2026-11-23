<script setup lang="ts">
import type { ObjectPayload, ObjectRef } from '@v8s/client';

import { nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';

import { getAgent } from '@/lib/agent.ts';
import { errorText } from '@/lib/mutations.ts';

import YamlPane from './YamlPane.vue';

const props = defineProps<{ target: ObjectRef }>();

// The payload (and with it any revealed values) lives in a shallowRef, not Pinia and not a deep ref. It is dropped when this
// pane goes away (drawer closed, tab changed) or the object changes.
const payload = shallowRef<ObjectPayload | null>(null);
const revealed = ref(false);
const loading = ref(false);
const error = ref('');
const pane = ref<{ mode: string; startEdit(): Promise<void>; reloadLive(): Promise<void> } | null>(null);
let seq = 0;

async function load(reveal: boolean) {
    const my = ++seq;
    loading.value = true;
    error.value = '';
    try {
        const t = props.target;
        const res = await getAgent().call<ObjectPayload>('object.get', {
            cluster: t.cluster,
            gvr: t.gvr,
            namespace: t.namespace,
            name: t.name,
            reveal: reveal || undefined
        });
        if (my !== seq) return;
        payload.value = res;
        revealed.value = reveal;
    } catch (e) {
        if (my !== seq) return;
        error.value = errorText(e);
        // Never keep values around after a failed refetch of a masked view.
        if (!reveal) payload.value = null;
    } finally {
        if (my === seq) loading.value = false;
    }
}

function drop() {
    seq++;
    payload.value = null;
    revealed.value = false;
}

onMounted(() => void load(false));
onBeforeUnmount(drop);
watch(
    () => `${props.target.cluster}|${props.target.namespace}|${props.target.name}`,
    () => {
        drop();
        void load(false);
    }
);

// "Reveal and edit": fetch the values, then open the editor on the revealed document. A failed reveal leaves the error shown and the view as it was.
const opening = ref(false);
async function revealAndEdit() {
    if (opening.value) return;
    opening.value = true;
    try {
        await load(true);
        if (error.value || !revealed.value) return;
        await nextTick(); // the pane gets the revealed payload before it opens the document
        await pane.value?.startEdit();
    } finally {
        opening.value = false;
    }
}

// Reload after a conflict whose server copy could not be revealed: reveal again, then let the pane reopen the document.
async function revealReload() {
    await load(true);
    if (error.value || !revealed.value) return;
    await nextTick();
    await pane.value?.reloadLive();
}

const editing = () => !!pane.value && pane.value.mode !== 'view';
</script>

<template>
    <div class="spane ypane">
        <p v-if="error" class="notice danger" role="alert">{{ error }}</p>
        <p v-if="loading && !payload" class="muted" role="status">Loading…</p>
        <template v-if="payload">
            <YamlPane
                ref="pane"
                :target="target"
                :payload="payload"
                :deleted="false"
                :frames="1"
                :masked="!revealed"
                @reveal-edit="revealAndEdit"
                @reveal-reload="revealReload"
                @synced="payload = $event"
                @applied="load(revealed)"
            >
                <template #status>
                    <span v-if="revealed" class="tag warning" role="status" title="The values were fetched; Mask drops them">values revealed</span>
                    <span v-else class="tag" role="status" title="Reveal fetches the values; with Decoded on they show as text">values masked</span>
                </template>
                <template #actions>
                    <button
                        v-if="!revealed"
                        class="btn sm"
                        type="button"
                        data-testid="reveal"
                        :disabled="loading || editing()"
                        :title="editing() ? 'Finish or discard your edit first' : 'Fetch the Secret again with its values'"
                        @click="load(true)"
                    >
                        Reveal
                    </button>
                    <button v-else class="btn sm" type="button" data-testid="mask" :disabled="loading || opening || editing()" @click="load(false)">
                        Mask
                    </button>
                </template>
            </YamlPane>
        </template>
    </div>
</template>

<style scoped>
.spane {
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-height: 0;
    flex: 1 1 0;
}
</style>
