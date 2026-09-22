import type { ReactNode } from 'react';

interface SparklineProps {
  readonly values: readonly number[];
  readonly label: string;
}

/** The viewport the polyline is drawn in. The element is scaled by css. */
const width = 120;
const height = 24;
const padding = 2;

/**
 * Sparkline draws a short series inline.
 *
 * The numbers in the panel say where the pipeline is now; the shape says which
 * way it is going, which is the difference between an outbox at forty and
 * falling and an outbox at forty and climbing. The accessible name carries both
 * the latest value and the peak, because a chart that only exists as a shape is
 * a chart half the operators cannot read.
 */
export function Sparkline({ values, label }: SparklineProps): ReactNode {
  if (values.length < 2) {
    // One point is not a trend, and a single dot invites reading one into it.
    return null;
  }

  const peak = Math.max(...values);
  const floor = Math.min(...values);
  const range = peak - floor;
  const latest = values[values.length - 1] ?? 0;
  const step = (width - 2 * padding) / (values.length - 1);

  const points = values
    .map((value, index) => {
      // A flat series has no range to scale against, so it sits on the middle
      // row: the top would read as a peak and the bottom as an empty chart.
      const fraction = range === 0 ? 0.5 : (value - floor) / range;
      const x = padding + index * step;
      const y = height - padding - fraction * (height - 2 * padding);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');

  return (
    <svg
      className="sparkline"
      viewBox={`0 0 ${width} ${height}`}
      role="img"
      aria-label={`${label}, now ${latest}, peak ${peak}, over ${values.length} samples`}
      preserveAspectRatio="none"
    >
      <polyline points={points} fill="none" stroke="currentColor" strokeWidth="1.5" />
    </svg>
  );
}
