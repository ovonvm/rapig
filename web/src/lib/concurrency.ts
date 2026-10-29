/**
 * Run an async worker over `items` with a fixed number of promises in flight.
 * Failures are collected per item instead of rejecting the whole batch, so the
 * caller can summarise "N done / M failed" and keep going.
 */

export interface BatchFailure {
  index: number
  error: unknown
}

export interface BatchResult {
  succeeded: number
  failures: BatchFailure[]
}

export async function runWithConcurrency<T>(
  items: readonly T[],
  limit: number,
  worker: (item: T, index: number) => Promise<unknown>,
): Promise<BatchResult> {
  const result: BatchResult = { succeeded: 0, failures: [] }
  if (items.length === 0) return result

  const width = Math.max(1, Math.min(limit, items.length))
  let cursor = 0

  async function drain(): Promise<void> {
    while (cursor < items.length) {
      const index = cursor
      cursor += 1
      const item: T | undefined = items[index]
      if (item === undefined) continue
      try {
        await worker(item, index)
        result.succeeded += 1
      } catch (error) {
        result.failures.push({ index, error })
      }
    }
  }

  await Promise.all(Array.from({ length: width }, () => drain()))
  return result
}
