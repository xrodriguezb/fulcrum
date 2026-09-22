import { useId, useState, type ReactNode, type SyntheticEvent } from 'react';
import { useCreateOrder } from './useOrders';
import type { ApiError } from '../../lib/requestState';

/** A customer identifier is required by the contract, so the form supplies one. */
const demoCustomerId = '11111111-2222-4333-8444-555555555555';

/** CreateOrderForm submits one line, which is what an operator does by hand. */
export function CreateOrderForm(): ReactNode {
  const skuId = useId();
  const quantityId = useId();
  const mutation = useCreateOrder();

  const [sku, setSku] = useState('');
  const [quantity, setQuantity] = useState('1');
  const [validation, setValidation] = useState<string | null>(null);

  const submit = (event: SyntheticEvent<HTMLFormElement, SubmitEvent>): void => {
    event.preventDefault();
    setValidation(null);

    const trimmed = sku.trim().toUpperCase();
    const parsedQuantity = Number.parseInt(quantity, 10);

    // The same rules the API enforces, applied here so an operator gets an
    // answer without a round trip. The API remains the authority.
    if (!/^[A-Z0-9][A-Z0-9-]{2,31}$/.test(trimmed)) {
      setValidation('A sku is three to thirty two characters, letters, digits and dashes.');
      return;
    }
    if (!Number.isInteger(parsedQuantity) || parsedQuantity < 1 || parsedQuantity > 1000) {
      setValidation('Quantity must be a whole number between 1 and 1000.');
      return;
    }

    mutation.mutate({
      customer_id: demoCustomerId,
      lines: [{ sku: trimmed, quantity: parsedQuantity }],
    });
  };

  return (
    <form onSubmit={submit} noValidate>
      <div className="field">
        <label htmlFor={skuId}>Sku</label>
        <input
          id={skuId}
          name="sku"
          value={sku}
          autoComplete="off"
          onChange={(event) => {
            setSku(event.target.value);
          }}
        />
      </div>

      <div className="field">
        <label htmlFor={quantityId}>Quantity</label>
        <input
          id={quantityId}
          name="quantity"
          type="number"
          min={1}
          max={1000}
          value={quantity}
          onChange={(event) => {
            setQuantity(event.target.value);
          }}
        />
      </div>

      <button type="submit" disabled={mutation.isPending}>
        {mutation.isPending ? 'Reserving...' : 'Reserve inventory'}
      </button>

      {/*
        One live region announces every outcome. Screen reader users otherwise
        learn nothing about a submission that changed a table further down the
        page.
      */}
      <div role="status" aria-live="polite" className="form-feedback">
        {validation !== null && <p className="error-text">{validation}</p>}
        {mutation.isError && <FailureMessage error={mutation.error} />}
        {mutation.isSuccess && <p className="success-text">Order {mutation.data.id} created.</p>}
      </div>
    </form>
  );
}

function FailureMessage({ error }: { readonly error: ApiError }): ReactNode {
  if (error.code === 'INVENTORY_INSUFFICIENT') {
    return (
      <p className="error-text">
        That quantity is no longer available. The order was not created and nothing was reserved.
      </p>
    );
  }
  if (error.code === 'VALIDATION_FAILED') {
    return <p className="error-text">The request was rejected: {error.detail}</p>;
  }
  return (
    <p className="error-text">
      {error.title}. <code>{error.code}</code>
      {error.traceId === undefined ? null : <> trace {error.traceId}</>}
    </p>
  );
}
