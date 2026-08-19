// Shared donut/pie chart geometry for SVG stroke-dasharray segments.

// buildPieSegments converts {key, color, count} entries into SVG circle
// segments (dasharray + offset) covering the full circumference. Zero-count
// entries are skipped; offsets accumulate so segments tile the ring.
export function buildPieSegments(entries, total, radius) {
  const circumference = 2 * Math.PI * radius;
  if (!total || total <= 0) return [];
  let offset = 0;
  return entries
    .filter((entry) => entry.count > 0)
    .map((entry) => {
      const fraction = entry.count / total;
      const arcLength = Math.max(fraction * circumference, 0);
      const segment = {
        ...entry,
        dasharray: `${arcLength} ${circumference}`,
        offset,
      };
      offset -= arcLength;
      return segment;
    });
}

// buildCoveragePieSegments is the covered/not-covered two-slice variant used
// by the test-coverage views.
export function buildCoveragePieSegments(
  covered,
  notCovered,
  total,
  coveredColor,
  notCoveredColor,
  radius
) {
  return buildPieSegments(
    [
      { key: 'covered', color: coveredColor, count: covered },
      { key: 'not-covered', color: notCoveredColor, count: notCovered },
    ],
    total,
    radius
  );
}
