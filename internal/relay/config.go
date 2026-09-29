package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"time"

	"github.com/wthomson/ctrlfreak/internal/protocol"
)

// Config controls the relay server. Load it from JSON (see config.example.json)
// or rely on the defaults plus environment overrides.
type Config struct {
	// Addr is the HTTPS listen address, e.g. ":8443".
	Addr string `json:"addr"`

	// TLSCert / TLSKey point to a certificate and key. If both are empty the
	// server generates a self-signed cert on startup (fine for first light;
	// use a real cert, e.g. Let's Encrypt, for production).
	TLSCert string `json:"tls_cert"`
	TLSKey  string `json:"tls_key"`

	// DBPath is the SQLite file location.
	DBPath string `json:"db_path"`

	// JWTSecret signs session tokens. If empty, a random secret is generated
	// at startup, which means every restart invalidates existing logins. Set a
	// stable value in production.
	JWTSecret string `json:"jwt_secret"`

	// TokenTTLHours is how long a login stays valid.
	TokenTTLHours int `json:"token_ttl_hours"`

	// Bootstrap admin, created only if the user table is empty.
	AdminUser string `json:"admin_user"`
	AdminPass string `json:"admin_pass"`

	// ICEServers is the STUN/TURN list handed to every peer. STUN lets peers
	// discover their public address for direct connections; TURN relays media
	// when a firewall blocks direct paths (the corporate-firewall case). See
	// ARCHITECTURE.md for how to stand up a TURN server.
	ICEServers []protocol.ICEServer `json:"ice_servers"`

	// MaxSessionsPerController caps how many remote screens ONE controller can
	// view at the same time. This is not a limit on how many machines you can
	// register (that is unlimited); it only bounds simultaneous live sessions,
	// since each open screen costs bandwidth and CPU. The browser reads this
	// value and sizes its grid to match. Mobile clients cap themselves at 1.
	MaxSessionsPerController int `json:"max_sessions_per_controller"`
}

// DefaultConfig returns sensible defaults. Firewall traversal works out of the
// box with public STUN; add a TURN server for the strict corporate case.
func DefaultConfig() Config {
	return Config{
		Addr:          ":8443",
		DBPath:        "ctrlfreak.db",
		TokenTTLHours: 24 * 7,
		AdminUser:     "wade",
		AdminPass:     "", // must be set on first run; see main.go
		ICEServers: []protocol.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
		MaxSessionsPerController: 10,
	}
}

// LoadConfig reads JSON config from path, falling back to defaults for any
// unset field. A missing file is not an error; you get pure defaults.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	// Decode over the defaults so omitted fields keep their default value.
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) tokenTTL() time.Duration {
	return time.Duration(c.TokenTTLHours) * time.Hour
}

// randomSecret returns a hex-encoded 32-byte secret.
func randomSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewRandomSecret returns a fresh hex-encoded 32-byte secret, for callers that
// want to persist one (e.g. the relay command's jwt secret file).
func NewRandomSecret() string { return randomSecret() }
