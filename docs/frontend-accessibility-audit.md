# Windshift Frontend Accessibility Audit

Work item: SOFT-33 — "Check Workspace Frontend from an Accessibility Perspective"
Verification date: 2026-08-12

This document is a baseline accessibility review of the Svelte frontend. It is
a findings report, not a remediation commit. The goal is to (1) record what is
already done well, (2) surface the concrete gaps most likely to fail automated
(aXe / Lighthouse) and manual accessibility testing, and (3) give focused,
file-referenced next steps. The review is static (source-level); it was not
possible to run a headed aXe/Lighthouse pass in this environment, so some
pass/fail calls are judgments from the markup patterns.

## Scope and method

- Scanned `frontend/src/lib` — the shared design-system components, pickers,
  layout, forms, dialogs, and feature surfaces.
- Looked for the standard WCAG/ARIA anti-patterns: non-semantic click targets,
  icon-only controls without accessible names, missing form labels, broken
  table/dialog semantics, focus trapping/return, keyboard operability, live
  regions, and reduced-motion handling.
- Grep + source reading (no browser). Findings below cite file + line.

## What is already done well

The codebase has a stronger accessibility baseline than most Svelte apps. The
shared primitives and several feature surfaces handle the hard cases properly:

1. **Modal focus management is first-class.** `ModalBackdrop.svelte`
   (`lib/components/ModalBackdrop.svelte`) saves focus on open, focuses the
   initial element, traps `Tab`/`Shift+Tab`, restores focus on close, and even
   restores on component teardown for conditionally-rendered dialogs. It sets
   `role="dialog"`, `aria-modal="true"` and optional `aria-labelledby`.
   Most dialogs (`dialogs/*`) pass `ariaLabelledBy` pointing at a real title.
2. **Keyboard-operable combobox pickers.** `lib/pickers/BasePicker.svelte`
   uses Melt's `createCombobox` and wires `role="combobox"`, `aria-expanded`,
   `aria-haspopup`, `aria-controls`, `aria-autocomplete`, a `role="listbox"`
   with `aria-selected` on Melt options, and full Arrow/Enter/Escape/Home/End
   key handling. Option browsing works without a mouse.
3. **Live regions for async feedback** are present in the right places:
   spinner/loader `role="status"`, error surfaces `role="alert"`
   (`lib/components/LazyRootView.svelte`, `LazyRootDialog.svelte`,
   `BrandedLoader.svelte`, analytics/timesheets/print views), and a polite
   board-move announcement (`collections/CollectionBoard.svelte:1437`).
4. **Switch and toggle semantics.** `Toggle.svelte` uses `role="switch"` with
   `aria-checked`; checked checkboxes render a real checked `input`.
5. **Accessible labels on many interactive icons** — the design-system
   `Label`/`FormField` pairing, `aria-label`/`title` on copy buttons, chip
   remove buttons, clear-selection buttons, icon triggers on menus
   (`DropdownMenu.svelte:165`), and `NavLink`.
6. **Fully keyboard-operable tooltips and menus** via Melt
   (`Tooltip.svelte`, `DropdownMenu.svelte` with `role="menu"/menuitem`).
7. **Localized `<html lang>`** is set from the active locale
   (`App.svelte:181`, `i18n.svelte.js:151`) and `index.html` starts `lang="en"`.
8. **Reduced-motion is respected** in the app-shell/boot
   (`index.html` `@media (prefers-reduced-motion: reduce)`) and in eight
   per-component blocks (`CommandPalette`, `MainApp`, `Homepage`,
   `CollectionNavigation`, workspace nav/welcome/look-and-feel, etc.).
9. **Real semantic elements** are used where it matters most (native
   `<button>`, `<input>`, `<label>`), so the core editing surfaces are
   keyboard-navigable.

## Prioritized findings

### High — likely to fail automated/manual reviews

**H1. Non-semantic click targets (div/span/table cell) with `onclick` only.**
These are invisible to keyboard users and, without `role`/`tabindex`, often to
screen readers. Each is explicitly `svelte-ignore`d today, i.e. the linter is
aware and the code passes silently:

- `lib/components/DataTable.svelte` — sortable `<th>` has `onclick`
  (`DataTable.svelte:176`) with **no** `role="button"`, `tabindex`, `aria-sort`,
  or `onkeydown`; row `<tr>` is clickable (`:199`) with no affordance, and the
  prev/next pagination `<button>`s (`:249`,`:260`) are icon-only with no
  `aria-label`. The th/tr are the most pervasive non-semantic controls in the app.
- `lib/components/Card.svelte:92-93` and `lib/components/Panel.svelte:52-53` —
  clickable `div` (no `role`/`tabindex`/`onkeydown`).
- `lib/widgets/dashboard/DailyBriefingWidget.svelte:64-66` — clickable
  `div onclick={handleClick}` with an `a11y_click_events_have_key_events`
  ignore.
- `lib/features/items/Comments.svelte:595`, `TodoList.svelte:459/470/535`,
  `ItemDetailSidebar.svelte:1153-1239`, `PersonalReview.svelte:303`,
  `JiraImportWizard.svelte:549`, `ConditionSetDetail.svelte:534-536`, and
  `WorkspaceSCMSettings.svelte:348` — clickable/`clickOutside` `div`s.
- `lib/widgets/Chart.svelte` — SVG chart wrapper is `mousemove`/`mouseleave`
  driven; data point cells have `tabindex="-1"`/`aria-label` (`:336-337`) but the
  chart itself is effectively pointer-only.

Suggestion: convert the high-traffic ones (DataTable header sort, Card/Panel
click, briefing widget) to real `<button>`/`<a>` or add `role`+`tabindex`+key
handlers, then remove the `svelte-ignore` comments so the linter guards them.

**H2. Missing `aria-labelledby` on several `role="dialog"` overlays.**
`ModalBackdrop` gains `aria-labelledby` only when callers pass
`ariaLabelledBy`. These callers do not, so the dialog has no accessible name:

- `lib/dialogs/CreateModal.svelte:402` (the main Create work-item dialog) — the
  header does render a visible title (`{t('createModal.new')} {currentTypeName}`)
  and a close button labelled "Close", but that title span has no `id` and no
  `ariaLabelledBy` is wired from the dialog, so the dialog has no accessible name.
- `lib/layout/CommandPalette.svelte:361` and its loading overlay in
  `lib/pages/MainAppOverlays.svelte:69`
- `lib/hub/HubCustomizePanel.svelte:17`, `lib/portal/PortalCustomizePanel.svelte:231`,
  `lib/portal/PortalProfile.svelte:274`, `lib/layout/Portal.svelte:660`

Suggestion: add `ariaLabelledBy` referencing a real heading id (CreateModal and
CommandPalette both have visible titles to point at).

**H3. `Select.svelte` trigger has no accessible name.**
`lib/components/Select.svelte` renders a `role="combobox"` `<button>` with no
`aria-label`, `aria-labelledby`, or surrounding `<label for>`. Out of the box it
is announced as an unlabelled popup button. Callers must supply context.

### Medium

**M1. DataTable pagination buttons are unlabelled.** Icon-only prev/next with
no `aria-label` (`DataTable.svelte:249-260`). Add `aria-label="{{prev}}"` /
`aria-label="{{next}}"` (localized).

**M2. `Toggle` hardcodes `aria-label="Toggle"` when no label prop is given.**
`Toggle.svelte:60` — an icon-less switch with no label will announce
"Toggle" with no meaning. Prefer requiring a label or deriving one.

**M3. No skip-to-content link.** Neither `index.html`, `App.svelte`, nor the
main layout renders a "Skip to main content" affordance. Keyboard users tab
through the full sidebar/nav before reaching content. The main content region
(`<main>` in `lib/layout/Portal.svelte`) exists and is the natural target.

**M4. Tab navigation is non-ARIA tabs.** `lib/components/TabNav.svelte` and
`PermissionsContainer.svelte` render buttons inside a `<nav aria-label="Tabs">`
with `aria-current="page"` but no `role="tablist"/tab`, `aria-selected`, or
arrow-key navigation. This is acceptable as a link-style nav, but is not a
"tablist"; screen readers may not announce selected state. Either add tablist
semantics or retitle the nav (`aria-label="Sections"`) to avoid confusion.

**M5. Form inputs without a visible/associative label.**
Several fields rely on `placeholder` alone (placeholder is not a valid label
and disappears on input): `MobileCreateDialog.svelte:840-842`,
`SCMProviderManager.svelte:696`, `IntegrationProviderManager.svelte:216-232`,
`ChannelFormConfig.svelte:139-145`, and `SearchInput.svelte` (which has no
`aria-label` either, though its placeholder is usually present). Wrap in
`FormField`/`Label` or pass an `aria-label`.

**M6. `SearchInput.svelte` has no `aria-label`.** The global search input
(`lib/components/SearchInput.svelte`) has only a placeholder. Add `aria-label`
(its icon is decorative).

### Low / polish

**L1. Dead reduced-motion CSS.** `design-system/animations.css` contains a
global `@media (prefers-reduced-motion: reduce)` block (`:310`) but that
file is **not imported** by `app.css`/`design-system/index.css` (only
referenced in a comment in `GlassButton.svelte`). The app relies on the eight
per-component blocks instead. Either wire `animations.css` into the bundle (so
its global keyframes/utilities and reduced-motion rule actually apply) or delete
it to avoid confusion. Note Svelte's built-in CSS `transition:`/`fly:`/`fade:`
directives (used in ~56 components) are **not** disabled by this CSS rule; only
the per-component blocks cover those.

**L2. Icon color contrast.** `text-gray-400`/`text-slate-400`/`text-subtlest`
are used for interactive icons in 16+ files; the `--ds-text-subtlest` token is
below the 3:1 contrast floor for some surfaces. Verify against the resolved
token value.

**L3. Consistent heading hierarchy.** Dialogs/panels that already have a
visible title should expose it via a real heading (`h1`/`h2`) so the
`aria-labelledby` in H2 points at a landmark, not a styled `div`.

## Recommended next steps

1. **Add automated checks** to `frontend/.github/workflows/frontend.yml`
   (currently lint + typecheck + build only). Cheapest high-signal additions:
   - `eslint-plugin-jsx-a11y`-style static rules are handled by Biome's a11y
     rules — currently **deactivated** (no `a11y` block in `frontend/biome.json`).
     Enable Biome's `a11y` rule group so the ~30 `svelte-ignore
     a11y_*`-suppressed violations become visible and auditable.
   - A `vitest-axe` pass over the shared components (Button, Input, Select,
     Checkbox, Toggle, DataTable, SearchInput, ModalBackdrop) in CI.
   - A periodic Lighthouse accessibility run against a deployed instance
     (requires a browser runtime, not present in this sandbox).
2. **Fix H1/H2/H3 first** — they are the highest-probability aXe failures and
   are confined to the shared primitives, so fixing them lifts every surface.
3. **Add skip-to-content** (M3) and `aria-label`s (M1/M6) — small, low-risk,
   broad-impact.
4. Remove now-stale `svelte-ignore a11y_*` comments once the underlying
   elements are fixed so the linter re-guards them.

## Non-verified items (need a browser)

- Live screen-reader announcements (NVDA/VoiceOver) of picker selection, board
  drag/drop, and the toast container (`role` present on toasts but not
  exercised here).
- Final colour-contrast ratios against the resolved `--ds-*` token values.
- Focus-visible visibility on every surface (CSS rings are styled, but no
  e2e pass confirmed a visible 2px ring for every interactive element).