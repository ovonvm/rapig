/**
 * M1 rule (docs/09-m1-plan.md §11): only files up to 32 MiB are pre-hashed in
 * the browser. The digest is sent at upload-init so the server can dedupe
 * without receiving any bytes.
 */
export const PREHASH_LIMIT_BYTES = 32 * 1024 * 1024

export async function sha256Hex(data: Blob): Promise<string> {
  const buffer = await data.arrayBuffer()
  const digest = await crypto.subtle.digest('SHA-256', buffer)
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('')
}
