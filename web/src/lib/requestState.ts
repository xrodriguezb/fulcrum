/**
 * Request state as a discriminated union.
 *
 * Three booleans can represent states that cannot happen, such as loading and
 * error at the same time, and every component then has to decide which one wins.
 * A union makes those states unrepresentable and forces each case to be rendered
 * deliberately.
 */
export type RequestState<T> =
  | { readonly status: 'idle' }
  | { readonly status: 'loading' }
  | { readonly status: 'empty' }
  | { readonly status: 'success'; readonly data: T }
  | { readonly status: 'stale'; readonly data: T; readonly reason: string }
  | { readonly status: 'error'; readonly error: ApiError };

/**
 * ApiError is a problem document, or a transport failure described like one.
 *
 * It extends Error because it is thrown: a rejected promise carrying a plain
 * object loses its stack and confuses every tool that expects an Error.
 */
export class ApiError extends Error {
  public readonly code: string;
  public readonly title: string;
  public readonly detail: string;
  public readonly status: number;
  public readonly traceId: string | undefined;

  public constructor(fields: {
    code: string;
    title: string;
    detail: string;
    status: number;
    traceId?: string | undefined;
  }) {
    super(`${fields.code}: ${fields.title}`);
    this.name = 'ApiError';
    this.code = fields.code;
    this.title = fields.title;
    this.detail = fields.detail;
    this.status = fields.status;
    this.traceId = fields.traceId;
  }
}

interface QueryLike<T> {
  readonly data: T | undefined;
  readonly error: unknown;
  readonly isPending: boolean;
  readonly isFetching: boolean;
}

/**
 * fromQuery maps a query result onto the union, including the partial case:
 * data that is present but known to be out of date is neither success nor error,
 * and showing it as either would mislead an operator.
 */
export function fromQuery<T>(query: QueryLike<T>, isEmpty: (data: T) => boolean): RequestState<T> {
  if (query.error) {
    // Data that is known to be out of date is neither success nor error.
    // Replacing a table an operator is reading with an error banner, because a
    // background refetch failed, hides the numbers they still need.
    if (query.data !== undefined && !isEmpty(query.data)) {
      return {
        status: 'stale',
        data: query.data,
        reason: 'These numbers could not be refreshed and may be out of date.',
      };
    }
    return { status: 'error', error: toApiError(query.error) };
  }
  if (query.isPending || query.data === undefined) {
    return { status: 'loading' };
  }
  if (isEmpty(query.data)) {
    return { status: 'empty' };
  }
  return { status: 'success', data: query.data };
}

/** toApiError normalises anything thrown into something renderable. */
export function toApiError(error: unknown): ApiError {
  if (error instanceof ApiError) {
    return error;
  }
  if (error instanceof Error) {
    return new ApiError({
      code: 'NETWORK_ERROR',
      title: 'The request could not be completed',
      detail: error.message,
      status: 0,
    });
  }
  return new ApiError({
    code: 'UNKNOWN_ERROR',
    title: 'The request could not be completed',
    detail: 'An unexpected error occurred.',
    status: 0,
  });
}
