package relay

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, no CGo
)

// Store wraps the SQLite database that holds users and hosts.
type Store struct {
	db *sql.DB
}

// User is an account. PassHash is a bcrypt hash; the plaintext password is
// never stored and cannot be recovered, by design. See SECURITY.md.
type User struct {
	ID        int64
	Username  string
	PassHash  string
	IsAdmin   bool
	CreatedAt time.Time
	// MustChange forces a password reset on next login (set when an admin
	// resets someone's password to a temporary value).
	MustChange bool
}

// Host is a machine a user has registered so it can be controlled later.
type Host struct {
	ID       int64
	Owner    string
	Name     string
	Platform string
	LastSeen time.Time
}

var ErrNotFound = errors.New("not found")
var ErrExists = errors.New("already exists")

// OpenStore opens (and migrates) the SQLite database at path.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: serialize writes, avoids "database is locked"
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS users (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  username    TEXT UNIQUE NOT NULL COLLATE NOCASE,
  pass_hash   TEXT NOT NULL,
  is_admin    INTEGER NOT NULL DEFAULT 0,
  must_change INTEGER NOT NULL DEFAULT 0,
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS hosts (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  owner      TEXT NOT NULL COLLATE NOCASE,
  name       TEXT NOT NULL,
  platform   TEXT NOT NULL DEFAULT '',
  last_seen  DATETIME,
  UNIQUE(owner, name)
);
CREATE TABLE IF NOT EXISTS audit (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  ts        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  actor     TEXT NOT NULL,
  action    TEXT NOT NULL,
  target    TEXT NOT NULL DEFAULT '',
  detail    TEXT NOT NULL DEFAULT ''
);`)
	return err
}

// EnsureAdmin creates the initial admin account if no users exist yet. It is
// called once at startup so there is always a way in.
func (s *Store) EnsureAdmin(username, password string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if err := s.CreateUser(username, password, true); err != nil {
		return err
	}
	s.Audit("system", "bootstrap_admin", username, "")
	return nil
}

// CreateUser adds an account with a freshly hashed password.
func (s *Store) CreateUser(username, password string, admin bool) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO users (username, pass_hash, is_admin) VALUES (?,?,?)`,
		username, string(hash), b2i(admin),
	)
	if err != nil {
		// modernc returns a generic error; detect the unique violation by text.
		return fmt.Errorf("%w: %v", ErrExists, err)
	}
	return nil
}

// Authenticate checks a username/password pair and returns the user on success.
func (s *Store) Authenticate(username, password string) (*User, error) {
	u, err := s.GetUser(username)
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PassHash), []byte(password)) != nil {
		return nil, errors.New("invalid credentials")
	}
	return u, nil
}

func (s *Store) GetUser(username string) (*User, error) {
	u := &User{}
	var admin, must int
	err := s.db.QueryRow(
		`SELECT id, username, pass_hash, is_admin, must_change, created_at
		   FROM users WHERE username = ? COLLATE NOCASE`, username,
	).Scan(&u.ID, &u.Username, &u.PassHash, &admin, &must, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.IsAdmin = admin == 1
	u.MustChange = must == 1
	return u, nil
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(
		`SELECT id, username, is_admin, must_change, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var admin, must int
		if err := rows.Scan(&u.ID, &u.Username, &admin, &must, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.IsAdmin = admin == 1
		u.MustChange = must == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes an account and all hosts it owns.
func (s *Store) DeleteUser(username string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE username = ? COLLATE NOCASE`, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, _ = s.db.Exec(`DELETE FROM hosts WHERE owner = ? COLLATE NOCASE`, username)
	return nil
}

// SetPassword changes a user's password. When temporary is true the user is
// forced to choose a new one at next login (used by admin resets).
func (s *Store) SetPassword(username, password string, temporary bool) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE users SET pass_hash = ?, must_change = ? WHERE username = ? COLLATE NOCASE`,
		string(hash), b2i(temporary), username,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetAdmin(username string, admin bool) error {
	res, err := s.db.Exec(
		`UPDATE users SET is_admin = ? WHERE username = ? COLLATE NOCASE`, b2i(admin), username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RegisterHost records (or refreshes) a host owned by a user.
func (s *Store) RegisterHost(owner, name, platform string) error {
	_, err := s.db.Exec(`
INSERT INTO hosts (owner, name, platform, last_seen) VALUES (?,?,?,?)
ON CONFLICT(owner, name) DO UPDATE SET platform=excluded.platform, last_seen=excluded.last_seen`,
		owner, name, platform, time.Now())
	return err
}

func (s *Store) ListHosts(owner string) ([]Host, error) {
	rows, err := s.db.Query(
		`SELECT id, owner, name, platform, last_seen FROM hosts WHERE owner = ? COLLATE NOCASE ORDER BY name`,
		owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Host
	for rows.Next() {
		var h Host
		var ls sql.NullTime
		if err := rows.Scan(&h.ID, &h.Owner, &h.Name, &h.Platform, &ls); err != nil {
			return nil, err
		}
		if ls.Valid {
			h.LastSeen = ls.Time
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Audit appends a line to the audit log. Every session start, file transfer,
// and admin action is recorded so there is always an honest record of access.
func (s *Store) Audit(actor, action, target, detail string) {
	_, _ = s.db.Exec(
		`INSERT INTO audit (actor, action, target, detail) VALUES (?,?,?,?)`,
		actor, action, target, detail)
}

// AuditEntry is one row of the access log.
type AuditEntry struct {
	TS     time.Time `json:"ts"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
}

func (s *Store) RecentAudit(limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(
		`SELECT ts, actor, action, target, detail FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.TS, &e.Actor, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
