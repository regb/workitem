# Web access model

`wi web` does not authenticate. It serves the operator API on an owner-only Unix socket, or on a loopback port, and relies on a proxy in front of it to decide who reaches it. This keeps one contract for every way in:

- The proxy authenticates, then forwards HTTP to the socket with the browser's Host header unchanged.
- Wi accepts only the declared `--access-url` Host and Origin, refuses cross-site requests, and requires same-origin JSON for mutations. These checks protect against other websites in the operator's browser, not against unauthenticated users; that is the proxy's job.
- When configured with `--identity-header`, Wi refuses requests without the proxy's user header and names control leases after that user. The header is trusted only because nothing but the proxy can reach the socket.

Supported today: Tailscale Serve (end-to-end encrypted between tailnet devices) and Cloudflare Tunnel with Access (Cloudflare terminates TLS and can read traffic).

An earlier version also required a bearer token in the URL fragment. It duplicated what the proxy does, could not identify users, and was removed.

## Future: paired devices over a relay

A native mobile app can be end-to-end encrypted through a relay we operate, because its code does not come from the relay (a browser page's would). The intended design fits the same contract:

1. Pairing on the machine: Wi shows a QR code with the machine's public key and relay ID; the phone sends back its key, which the machine adds to a local allowlist. Authorization lives on the machine, not the relay.
2. Both sides dial out to the relay, which only joins connections by machine ID. Inside, they run a handshake with the paired keys (Noise, or TLS with pinned keys). The relay sees metadata, but cannot read, alter, or impersonate either side.
3. A connector on the machine terminates that channel and forwards to the `wi web` socket, setting the identity header to the paired device's name. To Wi it is one more proxy.

Push notifications should carry no content, or a payload encrypted to the device. Until this exists, a mobile app can use the API over Tailscale.
