import { defineStore } from 'pinia'
import {
  cancelAccountDeletion as apiCancelAccountDeletion,
  changePassword as apiChangePassword,
  requestAccountDeletion as apiRequestAccountDeletion,
} from '../api/client'
import type { AccountDeletionTicket } from '../api/client'

export const useSettingsStore = defineStore('settings', () => {
  /** The server drops every other session when this succeeds. */
  async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
    await apiChangePassword({
      current_password: currentPassword,
      new_password: newPassword,
    })
  }

  /** Starts the deletion grace period; the cancel token is returned once (M5.2c). */
  function requestAccountDeletion(password: string): Promise<AccountDeletionTicket> {
    return apiRequestAccountDeletion(password)
  }

  /** Anonymous cancel page: restores the account when the token is still valid. */
  function cancelAccountDeletion(token: string): Promise<void> {
    return apiCancelAccountDeletion(token)
  }

  return { changePassword, requestAccountDeletion, cancelAccountDeletion }
})
