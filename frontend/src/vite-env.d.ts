/// <reference types="svelte" />
/// <reference types="vite/client" />

interface Window {
  __agentRuns?: {
    emit(): void;
    subscribe(fn: () => void): () => void;
  };
}
