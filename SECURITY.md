# CtrlFreak Security Model

CtrlFreak controls whole computers, so its security posture matters. This
document is honest about what protects you, and about two places where the design
intentionally differs from the first sketch.

## What protects a session

- **Passwords are hashed with bcrypt.** The database stores a one-way bcrypt hash,
  never the plaintext. Even someone with the database file (and even you, the
  admin) cannot read anyone's password. This is deliberate, and it is why the
  admin panel offers "reset password," not "view password." Storing recoverable
  passwords is the single most common way tools like this leak every user's
  credentials at once; CtrlFreak does not do it. If someone is locked out, you
  reset it to a temporary value and they set their own at next login.
- **Sessions are token-based.** Login returns a signed JWT with an expiry. It is
  presented on the signaling channel and to admin APIs. Set a stable
  `jwt_secret` so tokens survive relay restarts; leave it unset only for quick
  local testing.
- **Media is end-to-end encrypted.** Screen frames, input, and files travel over
  WebRTC DataChannels protected by DTLS, negotiated directly between the two
  peers. In the direct/STUN case the relay never sees this traffic; in the TURN
  case it sees only ciphertext.
- **Authorization on every connect.** A controller may open a session only to a
  host it owns, or to any host if it is an admin. The check happens on the relay
  at connect time and is logged. There is no path to a machine that skips it.
- **Everything is audited.** Sign ins (success and failure), session start/stop,
  file transfers, and every admin action are written to an append-only audit log
  visible in the admin panel. This is the honest record of who accessed what.

## Two deliberate design choices (and why)

Your first description included three features that, taken together, describe
covert surveillance software rather than a remote-support tool: sessions that
leave "no traces," a button to show the remote user a black screen, and silent
admin control of other people's machines with no indication to them. Two changes
were made on purpose:

1. **Sessions are visible, not hidden.** A controlled machine shows the normal
   session indicator. A support tool you would be comfortable installing on a
   family member's PC is one they can see is in use. Hidden-session and
   screen-blanking mechanisms have essentially no legitimate purpose on machines
   other than your own, and building them in would turn a useful tool into
   something that could create serious legal exposure for you (computer-access
   and interception laws apply even to software you wrote and distributed).

2. **Admin access is authorized and logged, not secret.** As admin you can reach
   any user's online machine, which is genuinely useful for helping your kids.
   That access is recorded in the audit log and the session is visible on the
   remote end, exactly as a professional remote-support tool behaves. The
   difference from the original sketch is only that it is on the record, which
   protects you as much as the user.

If you are supporting machines that are entirely your own, none of this costs you
anything: a visible indicator on your own PC is harmless, and the audit log is
just your own history.

## Hardening checklist for real use

- [ ] Set a strong `jwt_secret` (`openssl rand -hex 32`) and a strong admin
      password; pass both via environment variables, not the config file on disk.
- [ ] Put a real TLS certificate on the relay (Let's Encrypt) and run the host
      agents with `-verify` so they reject anything but your relay.
- [ ] Run the relay on its own port (443 is the most firewall-friendly) and
      expose nothing else.
- [ ] Give each person their own account; do not share logins. Keep admin to
      yourself.
- [ ] Review the audit log periodically.
- [ ] Keep the host agent binary from a trusted build; anyone you hand it to is
      trusting it, so treat your build pipeline accordingly.
- [ ] Consider short-lived TURN credentials rather than a static shared secret
      (see ARCHITECTURE.md).

## Threat notes

- The relay is the trust anchor for introductions and accounts. Protect the box
  it runs on and its database file.
- A self-signed relay cert plus hosts running without `-verify` is fine for first
  light but is vulnerable to a man-in-the-middle who can intercept the relay
  connection. Move to a real cert and `-verify` before using it over untrusted
  networks.
- WebRTC exposes peers' IP addresses to each other during ICE, which is inherent
  to peer-to-peer. Forcing all traffic through TURN (a `relay`-only ICE policy)
  hides host IPs from controllers at the cost of latency and TURN bandwidth, and
  can be added if you want it.
