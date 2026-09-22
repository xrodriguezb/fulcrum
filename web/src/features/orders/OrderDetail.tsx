import type { ReactNode } from 'react';

interface OrderDetailProps {
  readonly orderId: string;
  readonly onClose: () => void;
}

/** OrderDetail shows one order in full. */
export function OrderDetail({ orderId, onClose }: OrderDetailProps): ReactNode {
  // A stub with the real signature, so the specification compiles and fails on
  // behaviour rather than on a missing module.
  throw new Error(`OrderDetail is not implemented: ${orderId} ${typeof onClose}`);
}
