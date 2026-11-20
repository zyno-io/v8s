<script setup lang="ts">
import type { ProblemClassInfo } from '@v8s/client';

import { computed, ref } from 'vue';

import SmartSelect from '@/components/SmartSelect.vue';
import { errorText } from '@/lib/mutations.ts';
import { applyPresetPatch, currentClassSet, deletePresetPatch, presetDiff, presetsOf, savePresetPatch } from '@/lib/problems.ts';
import { groupList } from '@/lib/settings.ts';
import { listOptions } from '@/lib/smart-select.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useUiStore } from '@/stores/ui.ts';

const props = defineProps<{ classes: readonly ProblemClassInfo[] }>();
const emit = defineEmits<{ changed: [] }>();

const settings = useSettingsStore();
const ui = useUiStore();
const open = ref(false);
const pick = ref('');
const btn = ref<HTMLButtonElement | null>(null);

const presets = computed(() => Object.entries(presetsOf(settings.doc)).sort((a, b) => a[0].localeCompare(b[0])));
const groups = computed(() => groupList(settings.groups).map(([g]) => g));
const groupOptions = computed(() => listOptions(groups.value));
const target = computed(() => pick.value || groups.value[0] || '');

async function run(p: Record<string, unknown>, done: string) {
    try {
        await settings.patch(p);
        ui.toast('success', done);
        emit('changed');
    } catch (e) {
        ui.toast('danger', errorText(e));
    }
}
const save = () => target.value && run(savePresetPatch(target.value, currentClassSet(props.classes)), `Saved preset for ${target.value}`);
const apply = (g: string, p: { classes: Record<string, boolean> }) => run(applyPresetPatch(p, props.classes), `Applied preset ${g}`);
const remove = (g: string) => run(deletePresetPatch(g), `Deleted preset ${g}`);
function close() {
    open.value = false;
    btn.value?.focus();
}
</script>

<template>
    <div class="wrap" @keydown.esc.stop="open && close()">
        <button ref="btn" class="btn" type="button" aria-haspopup="dialog" :aria-expanded="open" data-testid="presets" @click="open = !open">
            Presets
        </button>
        <template v-if="open">
            <div class="back" @mousedown="close"></div>
            <div class="floating menu" role="dialog" aria-label="Problem class presets">
                <p class="muted help">
                    A preset stores which problem classes are on, for a cluster group (production shows everything, staging hides noise).
                </p>
                <div v-if="!presets.length" class="muted none">No presets yet.</div>
                <div v-for="[g, p] in presets" :key="g" class="pr">
                    <b class="gn">{{ g }}</b>
                    <span class="muted">{{ Object.values(p.classes).filter(Boolean).length }} on</span>
                    <span class="grow"></span>
                    <button
                        class="btn sm"
                        type="button"
                        :disabled="!presetDiff(p, classes)"
                        :title="`${presetDiff(p, classes)} switches would change`"
                        @click="apply(g, p)"
                    >
                        Apply
                    </button>
                    <button class="btn sm ghost" type="button" :aria-label="`Delete preset ${g}`" @click="remove(g)">Delete</button>
                </div>
                <div class="foot">
                    <template v-if="groups.length">
                        <label class="lbl" for="preset-group">Save current classes for</label>
                        <div class="save">
                            <SmartSelect
                                id="preset-group"
                                block
                                :model-value="target"
                                :options="groupOptions"
                                aria-label="Cluster group"
                                @update:model-value="pick = $event"
                            />
                            <button class="btn primary" type="button" @click="save">Save</button>
                        </div>
                    </template>
                    <p v-else class="muted none">Create a cluster group in the cluster selector to save a preset for it.</p>
                </div>
            </div>
        </template>
    </div>
</template>

<style scoped>
.wrap {
    position: relative;
}
.back {
    position: fixed;
    inset: 0;
    z-index: 20;
}
.menu {
    position: absolute;
    z-index: 21;
    right: 0;
    top: 36px;
    width: 340px;
    max-width: calc(100vw - 24px);
    padding: 10px;
    display: flex;
    flex-direction: column;
    gap: 6px;
}
.help,
.none {
    margin: 0;
    font-size: 12px;
}
.pr {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 32px;
}
.gn {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
}
.foot {
    border-top: 1px solid var(--border);
    padding-top: 8px;
    display: flex;
    flex-direction: column;
    gap: 6px;
}
.save {
    display: flex;
    gap: 8px;
}
</style>
