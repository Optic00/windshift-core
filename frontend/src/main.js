import { mount } from 'svelte';
import './app.css';
import { installContextPathTranslation } from './lib/runtime/contextPath.js';

installContextPathTranslation();

const { default: App } = await import('./App.svelte');

const app = mount(App, {
  target: document.getElementById('app'),
});

export default app;
