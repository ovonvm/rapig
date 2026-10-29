package sqlstore

import (
	"database/sql"
	"errors"
)

// isNoRows reports whether err is sql.ErrNoRows (wrapped or not).
func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
