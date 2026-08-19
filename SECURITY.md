# Security policy

Midgard is an experimental local runtime. Its current executor deliberately
runs trusted repository commands on the host and is not a containment or
multi-tenant security boundary. Do not use it with untrusted repositories or
credentials you cannot safely expose to an agent-controlled process.

Never report a credential, private repository URL, or suspected vulnerability
in a public issue. Use GitHub's private security advisory flow for this
repository. If that flow is unavailable, contact the maintainer privately
through GitHub before sharing technical details.
