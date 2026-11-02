# Pinned M5 macOS bootstrap recipe

`upstream-vanilla-tahoe.pkr.hcl` is copied from
[`cirruslabs/macos-image-templates` at `d6a3774b296fb80aa42c2a4885d6eb364a909ffd`](https://github.com/cirruslabs/macos-image-templates/blob/d6a3774b296fb80aa42c2a4885d6eb364a909ffd/templates/vanilla-tahoe.pkr.hcl).
The upstream project is MIT licensed. The copied file's SHA-256 is
`2beef2f46e600e6f36ac1dc272077bb5154c1ae2ceea3cbacf1ea3462faa652c`.

`mini-m5-packer-template` verifies that exact file and derives an M5-only
template. It pins the Tart plugin to 1.21.0, uses an already verified local
IPSW, binds a run-specific guest name and random bootstrap password, starts
the guest with Softnet, and omits upstream broad sudo, auto-login, and
Gatekeeper changes. The derived template still creates only a temporary
bootstrap account. The protected guest controller must harden that account
and prove network, GUI, and root-helper state before admitting retained inputs
or registering the M5 GitHub runner.
