package identity

import "rapig/internal/platform/passwd"

// hashPassword produces the PHC string stored in users.password_hash.
func hashPassword(password string) (string, error) { return passwd.Hash(password) }

// verifyPassword checks a password against a stored PHC string.
func verifyPassword(encoded, password string) error { return passwd.Verify(encoded, password) }
