/* Home-Screen install affordance.
 *
 * Chrome/Android/desktop fire a `beforeinstallprompt` event we can defer and
 * replay from a user gesture — a real one-tap install. iOS Safari has no such
 * API; the only path is the Share sheet → "Add to Home Screen", which a page
 * cannot trigger, so there we surface step-by-step instructions instead. */
import { writable } from 'svelte/store';
import { isStandalone } from './pushClient.js';

let deferredPrompt = null;

// Drives the iOS instructions sheet (rendered by MobileShell).
export const iosInstallOpen = writable(false);

if (typeof window !== 'undefined') {
  window.addEventListener('beforeinstallprompt', (e) => {
    // Stash it so we can prompt from an explicit user action later.
    e.preventDefault();
    deferredPrompt = e;
  });
  window.addEventListener('appinstalled', () => {
    deferredPrompt = null;
  });
}

/** iOS / iPadOS (incl. iPadOS reporting as Mac with touch). */
export function isIOS() {
  if (typeof navigator === 'undefined') return false;
  const ua = navigator.userAgent || '';
  return /iphone|ipad|ipod/i.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1);
}

/**
 * Whether to show the "Add to Home Screen" affordance: not already installed,
 * and either a deferred native prompt is available or we're on iOS (where we
 * fall back to instructions).
 */
export function installAvailable() {
  if (isStandalone()) return false;
  return deferredPrompt != null || isIOS();
}

/**
 * Trigger installation. Replays the native prompt where available; otherwise
 * opens the iOS instructions sheet. Returns the outcome.
 * @returns {Promise<'accepted'|'dismissed'|'instructions'|'unavailable'>}
 */
export async function requestInstall() {
  if (deferredPrompt) {
    deferredPrompt.prompt();
    const { outcome } = await deferredPrompt.userChoice;
    deferredPrompt = null;
    return outcome;
  }
  if (isIOS()) {
    iosInstallOpen.set(true);
    return 'instructions';
  }
  return 'unavailable';
}
