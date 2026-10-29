export const FOLDER_NAME_HINT = '1–255 个字符，不能包含 / \\\\ : * ? " < > |'

const FORBIDDEN_CHARS = /[\\\\/:*?"<>|]/
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/

/**
 * Mirrors the server's files.ValidateName: 1–255 runes, no path separators or
 * Windows-reserved characters, no control characters, no trailing dot/space
 * (Windows silently strips those, which would break lookups).
 * Returns a user-facing problem description, or null when the name is valid.
 */
export function validateNodeName(raw: string, label = '名称'): string | null {
  const name = raw.trim()

  if (name.length === 0) return `请输入${label}`
  if (Array.from(name).length > 255) return '名称不能超过 255 个字符'
  if (FORBIDDEN_CHARS.test(name)) return '名称不能包含 / \\\\ : * ? " < > | 这些字符'
  if (CONTROL_CHARS.test(name)) return '名称不能包含控制字符'
  if (name === '.' || name === '..') return '名称不能是 . 或 ..'
  if (/[. ]$/.test(name)) return '名称不能以空格或句点结尾'

  return null
}

/** New-folder dialog copy keeps saying "文件夹". */
export function validateFolderName(raw: string): string | null {
  return validateNodeName(raw, '文件夹名称')
}
