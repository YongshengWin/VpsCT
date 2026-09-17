// A failed connection is not evidence that the browser's session has expired.
export function isSessionUnauthorized(error: unknown): boolean {
  return error instanceof Error && "status" in error && error.status === 401;
}

export async function loadSession<T>(fetchUser: () => Promise<T>): Promise<T | null> {
  try {
    return await fetchUser();
  } catch (error) {
    if (isSessionUnauthorized(error)) return null;
    throw error;
  }
}

export function retrySession(failureCount: number, error: unknown): boolean {
  return !isSessionUnauthorized(error) && failureCount < 2;
}
