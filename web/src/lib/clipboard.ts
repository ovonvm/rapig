/**
 * Clipboard helper for share links. `navigator.clipboard` needs a secure
 * context and permission, so always keep a selection-based fallback.
 */
export async function copyText(text: string): Promise<boolean> {
  if (!text) return false

  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Permission denied / not focused: try the legacy path below.
  }

  return legacyCopy(text)
}

function legacyCopy(text: string): boolean {
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.setAttribute('aria-hidden', 'true')
  area.style.position = 'fixed'
  area.style.top = '-1000px'
  area.style.opacity = '0'
  document.body.append(area)
  area.select()
  area.setSelectionRange(0, area.value.length)

  let copied = false
  try {
    copied = document.execCommand('copy')
  } catch {
    copied = false
  }

  area.remove()
  return copied
}

/** Select the text of an input so the visitor can copy it by hand. */
export function selectInputText(input: HTMLInputElement | null): void {
  if (!input) return
  input.focus()
  input.select()
}
