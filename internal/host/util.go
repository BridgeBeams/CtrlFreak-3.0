package host

import "crypto/tls"

// insecureTLS lets the host connect to a relay using a self-signed certificate,
// which is the default for a fresh install. Once you put a real certificate on
// the relay (recommended, see ARCHITECTURE.md), set VerifyRelayCert to true via
// the -verify flag so this returns a verifying config instead.
var VerifyRelayCert = false

func insecureTLS() *tls.Config {
	if VerifyRelayCert {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &tls.Config{InsecureSkipVerify: true} // #nosec G402 - opt-in for self-signed relay
}
