import { defineStore } from 'pinia';
import { ref, shallowRef, watch } from 'vue';

export type ThemeMode = 'system' | 'light' | 'dark';

export interface DrawerTarget {
    id: number;
    name: string;
    cluster: string;
    namespace: string;
}

function loadTheme(): ThemeMode {
    try {
        const v = localStorage.getItem('v8s:theme');
        return v === 'light' || v === 'dark' ? v : 'system';
    } catch {
        return 'system';
    }
}

function applyTheme(mode: ThemeMode) {
    const cl = document.documentElement.classList;
    cl.remove('light', 'dark');
    if (mode !== 'system') cl.add(mode);
    try {
        if (mode === 'system') localStorage.removeItem('v8s:theme');
        else localStorage.setItem('v8s:theme', mode);
    } catch {
        // ignore: preference just will not persist
    }
}

/** UI state only. Datasets never live in Pinia. */
export const useUiStore = defineStore('ui', () => {
    const theme = ref<ThemeMode>(loadTheme());
    const commandBarOpen = ref(false);
    const shortcutsOpen = ref(false);
    const drawer = shallowRef<DrawerTarget | null>(null);

    watch(theme, applyTheme);

    function cycleTheme() {
        theme.value = theme.value === 'system' ? 'light' : theme.value === 'light' ? 'dark' : 'system';
    }

    return { theme, commandBarOpen, shortcutsOpen, drawer, cycleTheme };
});
