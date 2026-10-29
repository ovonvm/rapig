/**
 * Storage location a new root folder can be created on (M5.3).
 * `GET /storage/policies` only ever lists enabled policies, so the dialog never
 * has to filter; `is_default` decides the preselected option.
 */
export interface StoragePolicyOption {
  id: number
  name: string
  is_default: boolean
  enabled: boolean
}

/** Preferred option: the instance default, else the first enabled policy. */
export function defaultStoragePolicyId(policies: readonly StoragePolicyOption[]): number | null {
  const fallback = policies.find((policy) => policy.enabled)
  const preferred = policies.find((policy) => policy.enabled && policy.is_default)
  return (preferred ?? fallback)?.id ?? null
}
