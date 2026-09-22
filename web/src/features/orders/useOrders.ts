import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
} from '@tanstack/react-query';
import {
  api,
  newIdempotencyKey,
  type CreateOrderRequest,
  type Order,
  type OrderPage,
} from '../../api/client';
import { toApiError, type ApiError } from '../../lib/requestState';

export const ordersKey = ['orders'] as const;

/** useOrders reads the order list. */
export function useOrders() {
  return useQuery({
    queryKey: ordersKey,
    queryFn: () => api.listOrders(),
  });
}

interface OptimisticContext {
  readonly previous: OrderPage | undefined;
  readonly temporaryId: string;
}

/**
 * useCreateOrder inserts the order optimistically and rolls it back on failure.
 *
 * The rollback matters most for 409 INVENTORY_INSUFFICIENT, which is not an
 * error in the system but an answer from it: the stock went while the operator
 * was typing. The optimistic row disappears and the form states the reason
 * inline, rather than in a toast that vanishes before it is read.
 */
export function useCreateOrder(): UseMutationResult<
  Order,
  ApiError,
  CreateOrderRequest,
  OptimisticContext
> {
  const queryClient = useQueryClient();

  return useMutation<Order, ApiError, CreateOrderRequest, OptimisticContext>({
    mutationFn: async (request: CreateOrderRequest) => {
      try {
        return await api.createOrder(request, newIdempotencyKey());
      } catch (cause) {
        throw toApiError(cause);
      }
    },

    onMutate: async (request: CreateOrderRequest) => {
      await queryClient.cancelQueries({ queryKey: ordersKey });
      const previous = queryClient.getQueryData<OrderPage>(ordersKey);
      const temporaryId = `pending-${newIdempotencyKey()}`;

      const optimistic: Order = {
        id: temporaryId,
        customer_id: request.customer_id,
        status: 'pending',
        total_cents: 0,
        currency: 'EUR',
        lines: request.lines.map((line) => ({
          sku: line.sku,
          quantity: line.quantity,
          unit_price_cents: 0,
        })),
        created_at: new Date().toISOString(),
      };

      queryClient.setQueryData<OrderPage>(ordersKey, (current) => {
        if (!current) {
          return { items: [optimistic], total: 1, limit: 20, offset: 0 };
        }
        return { ...current, items: [optimistic, ...current.items], total: current.total + 1 };
      });

      return { previous, temporaryId };
    },

    onError: (_error, _request, context) => {
      if (context?.previous !== undefined) {
        queryClient.setQueryData<OrderPage>(ordersKey, context.previous);
        return;
      }
      queryClient.setQueryData<OrderPage>(ordersKey, (current) =>
        current === undefined
          ? current
          : {
              ...current,
              items: current.items.filter((order) => order.id !== context?.temporaryId),
              total: Math.max(current.total - 1, 0),
            },
      );
    },

    onSuccess: (created, _request, context) => {
      // The optimistic row is replaced rather than appended, so the list does not
      // show the same order twice between the response and the refetch.
      queryClient.setQueryData<OrderPage>(ordersKey, (current) => {
        if (!current) {
          return { items: [created], total: 1, limit: 20, offset: 0 };
        }
        return {
          ...current,
          items: current.items.map((order) => (order.id === context.temporaryId ? created : order)),
        };
      });
    },

    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ordersKey });
    },
  });
}
