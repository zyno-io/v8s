/// <reference types="vite/client" />

interface Window {
    /** Present when running inside the Electron shell (see packages/desktop/src/preload.ts). */
    v8sDesktop?: { getAgent(): Promise<{ url: string; token: string }> };
}
