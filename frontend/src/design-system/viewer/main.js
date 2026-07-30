import './viewer.css';
import { mount } from 'svelte';
import { i18n } from '../../lib/stores/i18n.svelte.js';
import App from './App.svelte';

await i18n.init();

const app = mount(App, {
  target: document.getElementById('app'),
});

export default app;
