/// <reference types="svelte" />
/// <reference types="vite/client" />

interface Window {
  __actionMutations?: {
    emit(actionId: number | string): void;
    subscribe(fn: (actionId: number) => void): () => void;
  };
}
