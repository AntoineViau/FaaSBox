/**
 * What a failed call can actually tell the user.
 *
 * PocketBase puts the useful text in the response **body**: the message of a
 * refused cron expression lands there, and so does the one naming a refused
 * function name. `HttpErrorResponse.message` only restates the status, so
 * reading it alone drops exactly what the user needs to fix the input — a
 * "Failed to save: Http failure response ... 400 Bad Request" says nothing
 * about the underscore that caused it.
 */
export function errorText(error: unknown): string {
  const failure = error as { error?: { message?: unknown }; message?: unknown } | null;
  const body = failure?.error;
  if (body && typeof body.message === 'string' && body.message) {
    return body.message;
  }
  if (typeof failure?.message === 'string' && failure.message) {
    return failure.message;
  }
  return 'Unknown error';
}
