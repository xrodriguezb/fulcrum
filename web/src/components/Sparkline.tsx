import type { ReactNode } from 'react';

interface SparklineProps {
  readonly values: readonly number[];
  readonly label: string;
}

/** Sparkline draws a short series inline. */
export function Sparkline({ values, label }: SparklineProps): ReactNode {
  throw new Error(`Sparkline is not implemented: ${label} ${values.length}`);
}
