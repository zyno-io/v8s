import { contextBridge, ipcRenderer } from 'electron';

// The only surface the renderer gets. The token never touches a URL or the disk.
contextBridge.exposeInMainWorld('v8sDesktop', {
    getAgent: (): Promise<{ url: string; token: string }> => ipcRenderer.invoke('v8s:get-agent')
});
