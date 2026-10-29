package sqlstore

import (
	"context"
	"testing"

	domainidentity "rapig/internal/domain/identity"
	"rapig/internal/platform/apperr"
)

// createIdentifiedUser inserts an account that carries an e-mail and a phone
// number, mirroring what registration stores.
func createIdentifiedUser(t *testing.T, db *DB, username, email, phone string) domainidentity.User {
	t.Helper()
	ctx := context.Background()
	roleID, err := db.RoleIDByCode(ctx, "user")
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	u, err := db.CreateUser(ctx, domainidentity.User{
		Username:       username,
		UsernameFolded: domainidentity.FoldUsername(username),
		DisplayName:    username,
		PasswordHash:   "not-a-real-hash",
		RoleID:         roleID,
		Status:         domainidentity.StatusActive,
		QuotaBytes:     1 << 30,
		Email:          email,
		EmailFolded:    domainidentity.FoldEmail(email),
		Phone:          phone,
		PhoneFolded:    domainidentity.FoldPhone(phone),
	})
	if err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	return u
}

func TestUserIdentifiersRoundTrip(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()

	u := createIdentifiedUser(t, db, "alice", "Alice@Example.COM", "13800138000")
	if u.Email != "Alice@Example.COM" || u.EmailFolded != "alice@example.com" {
		t.Fatalf("email not stored: %+v", u)
	}
	if u.Phone != "13800138000" || u.PhoneFolded != "13800138000" {
		t.Fatalf("phone not stored: %+v", u)
	}
	if u.IsRoot {
		t.Fatalf("a plain account must not be root: %+v", u)
	}

	byEmail, err := db.UserByEmail(ctx, domainidentity.FoldEmail("ALICE@example.com"))
	if err != nil {
		t.Fatalf("user by email: %v", err)
	}
	if byEmail.ID != u.ID {
		t.Fatalf("user by email = %d, want %d", byEmail.ID, u.ID)
	}
	byPhone, err := db.UserByPhone(ctx, "13800138000")
	if err != nil {
		t.Fatalf("user by phone: %v", err)
	}
	if byPhone.ID != u.ID {
		t.Fatalf("user by phone = %d, want %d", byPhone.ID, u.ID)
	}
	if _, err := db.UserByEmail(ctx, "nobody@example.com"); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("unknown email = %v, want not_found", err)
	}
	if _, err := db.UserByPhone(ctx, "13900139000"); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("unknown phone = %v, want not_found", err)
	}
}

func TestRootFlagRoundTrip(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()

	u := seedUser(t, db, "plain", "user")
	if u.IsRoot {
		t.Fatalf("seeded account is root: %+v", u)
	}
	roleID, err := db.RoleIDByCode(ctx, "admin")
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	root, err := db.CreateUser(ctx, domainidentity.User{
		Username:       "root",
		UsernameFolded: "root",
		DisplayName:    "root",
		PasswordHash:   "not-a-real-hash",
		RoleID:         roleID,
		Status:         domainidentity.StatusActive,
		IsRoot:         true,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	reloaded, err := db.UserByID(ctx, root.ID)
	if err != nil {
		t.Fatalf("reload root: %v", err)
	}
	if !reloaded.IsRoot {
		t.Fatalf("root flag lost: %+v", reloaded)
	}
}

// TestOptionalIdentifiersAreNull keeps the partial unique indexes usable: two
// accounts without an e-mail or phone must coexist.
func TestOptionalIdentifiersAreNull(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()

	first := seedUser(t, db, "first", "user")
	second := seedUser(t, db, "second", "user")
	if first.Email != "" || first.Phone != "" {
		t.Fatalf("unexpected identifiers: %+v", first)
	}
	if _, err := db.UserByEmail(ctx, ""); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("empty email lookup = %v, want not_found", err)
	}
	if _, err := db.UserByPhone(ctx, "  "); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("blank phone lookup = %v, want not_found", err)
	}
	if second.ID == 0 {
		t.Fatal("second account was not created")
	}
}

func TestIdentifierTaken(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()

	u := createIdentifiedUser(t, db, "alice", "alice@example.com", "13800138000")

	cases := []struct {
		name        string
		identifiers []string
		want        bool
	}{
		{"username", []string{u.UsernameFolded}, true},
		{"email", []string{u.EmailFolded}, true},
		{"phone", []string{u.PhoneFolded}, true},
		{"cross namespace: username in email slot", []string{"other", u.EmailFolded}, true},
		{"cross namespace: phone in username slot", []string{u.PhoneFolded, "x@example.com"}, true},
		{"unknown", []string{"nobody", "nobody@example.com", "+8613900139000"}, false},
		{"empty identifiers", []string{"", ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.IdentifierTaken(ctx, tc.identifiers...)
			if err != nil {
				t.Fatalf("identifier taken: %v", err)
			}
			if got != tc.want {
				t.Fatalf("identifier taken = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIdentifierUniqueIndexes proves the database, not only the application
// check, refuses duplicate e-mail and phone numbers.
func TestIdentifierUniqueIndexes(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()

	createIdentifiedUser(t, db, "alice", "alice@example.com", "13800138000")

	roleID, err := db.RoleIDByCode(ctx, "user")
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	insert := func(username, email, phone string) error {
		_, err := db.CreateUser(ctx, domainidentity.User{
			Username:       username,
			UsernameFolded: domainidentity.FoldUsername(username),
			DisplayName:    username,
			PasswordHash:   "not-a-real-hash",
			RoleID:         roleID,
			Status:         domainidentity.StatusActive,
			Email:          email,
			EmailFolded:    domainidentity.FoldEmail(email),
			Phone:          phone,
			PhoneFolded:    domainidentity.FoldPhone(phone),
		})
		return err
	}
	if err := insert("bob", "alice@example.com", ""); err == nil {
		t.Fatal("duplicate e-mail accepted by the unique index")
	}
	if err := insert("carol", "", "13800138000"); err == nil {
		t.Fatal("duplicate phone accepted by the unique index")
	}
	if err := insert("alice", "", ""); err == nil {
		t.Fatal("duplicate username accepted by the unique index")
	}
	// Different identifiers stay fine.
	if err := insert("dave", "dave@example.com", "13900139000"); err != nil {
		t.Fatalf("distinct identifiers rejected: %v", err)
	}
}

// TestListUsersByIdentifier checks the admin user search finds accounts by
// e-mail or phone as well as by name.
func TestListUsersByIdentifier(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()

	createIdentifiedUser(t, db, "alice", "Alice@Example.COM", "13800138000")
	createIdentifiedUser(t, db, "bob", "bob@example.org", "13900139000")

	for _, q := range []string{"alice@example.com", "ALICE@", "1380013", "alice"} {
		items, total, err := db.ListUsers(ctx, q, 50, 0)
		if err != nil {
			t.Fatalf("list users %q: %v", q, err)
		}
		if total != 1 || len(items) != 1 || items[0].Username != "alice" {
			t.Fatalf("query %q = %d/%d items, want only alice", q, total, len(items))
		}
	}
	items, total, err := db.ListUsers(ctx, "example", 50, 0)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("query example = %d items, want both accounts", total)
	}
}
