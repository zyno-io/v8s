<script setup lang="ts">
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { yaml } from '@codemirror/lang-yaml';
import { bracketMatching, foldGutter, HighlightStyle, indentOnInput, syntaxHighlighting } from '@codemirror/language';
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search';
import { Compartment, EditorState } from '@codemirror/state';
import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from '@codemirror/view';
import { tags as t } from '@lezer/highlight';
import { onBeforeUnmount, onMounted, ref, watch } from 'vue';

const props = defineProps<{ initial: string; readOnly: boolean; label: string }>();
const emit = defineEmits<{ dirty: [dirty: boolean] }>();

const host = ref<HTMLDivElement | null>(null);
let view: EditorView | null = null;
const editable = new Compartment();
let base = props.initial;

// Colours come from the design tokens, so the editor follows light/dark with no theme branching.
const theme = EditorView.theme({
    '&': { height: '100%', fontSize: '12px', color: 'var(--text)', backgroundColor: 'var(--surface)' },
    '.cm-scroller': { fontFamily: "ui-monospace, 'SF Mono', 'JetBrains Mono', Menlo, Consolas, monospace", lineHeight: '1.55' },
    '.cm-content': { caretColor: 'var(--focus)' },
    '.cm-gutters': { backgroundColor: 'var(--bg)', color: 'var(--text3)', border: 'none', borderRight: '1px solid var(--border)' },
    '.cm-activeLine': { backgroundColor: 'var(--hover)' },
    '.cm-activeLineGutter': { backgroundColor: 'var(--s2)' },
    '&.cm-focused': { outline: '2px solid var(--focus)', outlineOffset: '-2px' },
    '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': { backgroundColor: 'var(--accent-soft)' },
    '.cm-cursor': { borderLeftColor: 'var(--focus)' },
    '.cm-selectionMatch': { backgroundColor: 'var(--s3)' },
    '.cm-panels': { backgroundColor: 'var(--bg)', color: 'var(--text)' },
    '.cm-searchMatch': { backgroundColor: 'var(--warning-soft)', outline: '1px solid var(--warning-b)' }
});

const highlight = HighlightStyle.define([
    { tag: [t.propertyName, t.definition(t.propertyName)], color: 'var(--link)' },
    { tag: [t.string, t.special(t.string)], color: 'var(--success)' },
    { tag: [t.number, t.bool, t.null], color: 'var(--warning)' },
    { tag: [t.keyword, t.atom], color: 'var(--release)' },
    { tag: [t.comment, t.lineComment], color: 'var(--text3)', fontStyle: 'italic' },
    { tag: [t.meta, t.separator, t.punctuation], color: 'var(--text3)' }
]);

function make(doc: string) {
    return EditorState.create({
        doc,
        extensions: [
            lineNumbers(),
            highlightActiveLineGutter(),
            foldGutter(),
            history(),
            drawSelection(),
            indentOnInput(),
            bracketMatching(),
            highlightActiveLine(),
            highlightSelectionMatches(),
            yaml(),
            syntaxHighlighting(highlight),
            theme,
            keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap, indentWithTab]),
            editable.of([EditorView.editable.of(!props.readOnly), EditorState.readOnly.of(props.readOnly)]),
            EditorView.contentAttributes.of({ 'aria-label': props.label, 'aria-multiline': 'true' }),
            EditorView.updateListener.of(u => {
                if (u.docChanged && !props.readOnly) emit('dirty', u.state.doc.toString() !== base);
            })
        ]
    });
}

onMounted(() => {
    view = new EditorView({ state: make(props.initial), parent: host.value! });
});
onBeforeUnmount(() => view?.destroy());

function setValue(text: string) {
    if (!view) return;
    base = text;
    view.setState(make(text));
}

watch(
    () => props.initial,
    v => {
        if (v !== base) setValue(v);
    }
);
watch(
    () => props.readOnly,
    ro => view?.dispatch({ effects: editable.reconfigure([EditorView.editable.of(!ro), EditorState.readOnly.of(ro)]) })
);

defineExpose({
    getValue: () => view?.state.doc.toString() ?? props.initial,
    setValue,
    focus: () => view?.focus()
});
</script>

<template>
    <div ref="host" class="cm-host"></div>
</template>

<style scoped>
.cm-host {
    height: 100%;
    min-height: 0;
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
}
</style>
