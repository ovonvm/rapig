/**
 * Login identifiers are shared by registration, the login page and the admin
 * "create user" dialog, so the rules live in one place. The server owns the
 * final say; these checks only exist to fail fast in the UI.
 */

export const USERNAME_MIN = 2
export const USERNAME_MAX = 32
export const EMAIL_MAX = 254
export const PHONE_MIN_DIGITS = 6
export const PHONE_MAX_DIGITS = 20

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/

/** Trim and lowercase an e-mail so it compares the way the server does. */
export function normalizeEmail(raw: string): string {
  return raw.trim().toLowerCase()
}

/** Strip spaces, dashes and parentheses; keep an optional leading `+`. */
export function normalizePhone(raw: string): string {
  const text = raw.trim().replace(/[\s()-]/g, '')
  return text
}

export function isValidEmail(raw: string): boolean {
  const value = normalizeEmail(raw)
  return value.length > 0 && value.length <= EMAIL_MAX && EMAIL_PATTERN.test(value)
}

export function isValidPhone(raw: string): boolean {
  const value = normalizePhone(raw)
  if (!/^\+?\d+$/.test(value)) return false
  const digits = value.replace(/^\+/, '').length
  return digits >= PHONE_MIN_DIGITS && digits <= PHONE_MAX_DIGITS
}

/** `null` means valid (or empty, which is allowed everywhere). */
export function validateUsernameField(raw: string, required = false): string | null {
  const value = raw.trim()
  if (!value) return required ? '请输入用户名' : null
  const length = Array.from(value).length
  if (length < USERNAME_MIN || length > USERNAME_MAX) {
    return `用户名需为 ${USERNAME_MIN}–${USERNAME_MAX} 个字符`
  }
  if (/[\s@]/.test(value)) return '用户名不能包含空格或 @'
  return null
}

export function validateEmailField(raw: string): string | null {
  const value = raw.trim()
  if (!value) return null
  if (value.length > EMAIL_MAX) return `邮箱不能超过 ${EMAIL_MAX} 个字符`
  if (!isValidEmail(value)) return '请输入有效的邮箱地址'
  return null
}

export function validatePhoneField(raw: string): string | null {
  const value = raw.trim()
  if (!value) return null
  if (!isValidPhone(value)) return `请输入有效的手机号（${PHONE_MIN_DIGITS}–${PHONE_MAX_DIGITS} 位数字，可带 + 前缀）`
  return null
}

/** Normalises the three optional identifiers into an API payload. */
export function identifierPayload(input: { username: string; email: string; phone: string }): {
  username?: string
  email?: string
  phone?: string
} {
  const payload: { username?: string; email?: string; phone?: string } = {}
  const username = input.username.trim()
  const email = normalizeEmail(input.email)
  const phone = normalizePhone(input.phone)
  if (username) payload.username = username
  if (email) payload.email = email
  if (phone) payload.phone = phone
  return payload
}
