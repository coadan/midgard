# Model protocol pin

Midgard vendors Bragi commit
`9d8219b91a06be0bbdcb7c2b07c1e85766feea24` and the exact bytes of
`profiles/midgard-v1.json` from that commit. The embedded profile fingerprint is
`sha256:3d7997c9ee4f9823d46f54370a92b1f62bef38470f6b30b9e7e3298003f315fb`.

The `replace bragi => ../bragi` directive is the local development source for
refreshing the vendored module. Normal builds use `vendor/bragi`; they do not
load code or profile data from the adjacent checkout at runtime.

An upgrade must update the pseudo-version, vendor tree, embedded profile, this
pin, and the protocol conformance fixtures together.
