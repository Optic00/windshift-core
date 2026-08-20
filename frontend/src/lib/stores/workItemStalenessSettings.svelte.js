const DEFAULT_STALE_AFTER_DAYS = 30;

let staleAfterDays = $state(DEFAULT_STALE_AFTER_DAYS);

function hydrate(settings) {
  const days = Number(settings?.stale_after_days);
  staleAfterDays = Number.isInteger(days) && days > 0 ? days : DEFAULT_STALE_AFTER_DAYS;
}

export const workItemStalenessSettings = {
  get staleAfterDays() {
    return staleAfterDays;
  },
  hydrate,
};
