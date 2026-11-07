# M5 macOS guest bootstrap runtime

The M5 rehearsal uses a disposable macOS VirtualMac because it exercises Mac-specific restore, `launchd`, firewall, Screen Sharing, and account behavior. The guest is created through the protected M5 executor; its creation is only an unqualified setup step.

## Why the guest needs Command Line Tools

A clean macOS install can leave `/usr/bin/git` and `/usr/bin/python3` as Apple developer-tool launch stubs. The first Packer helper needs Python, and later guest checkout, registration, launchd, and qualification steps need both Git and Python. Without Command Line Tools, macOS prompts to install them interactively, which cannot complete inside the unattended protected workflow.

## Pinned bootstrap

The guest bootstrap helper is checked by SHA-256 during protected host preflight and again through the source revision bound to the signed M5 intent. It checks for the exact VirtualMac/macOS 26.6.2 target, selects only the Apple Software Update label `Command Line Tools for Xcode 26.6-26.6`, and requires receipt `com.apple.pkg.CLTools_Executables` version `26.6.0.0.1781586589`. It rejects an unexpected existing developer directory or receipt, bounds discovery and installation, and verifies `/usr/bin/python3`, `/usr/bin/git`, the required Python standard library, and the selected developer directory before the existing guest helpers run.

The current path relies on Apple's Software Update catalog and exact product label plus package receipt. The package bytes are not pinned locally; that is weaker artifact identity than an offline Apple-signed package pinned by digest. The receipt, source SHA, helper digest, signed run intent, and successful guest readback must all be retained with the protected rehearsal evidence.

Apple documents that a fresh macOS install can prompt to install Command Line Tools when a tool such as `git` is first invoked, that `softwareupdate` lists compatible releases, and that `pkgutil --pkg-info=com.apple.pkg.CLTools_Executables` reports the installed package version: [Installing the Command Line Tools](https://developer.apple.com/documentation/xcode/installing-the-command-line-tools/). Apple also documents selecting a developer directory with `xcode-select`: [Configuring command-line tools settings](https://developer.apple.com/documentation/xcode/configuring-command-line-tools-settings).

## Qualification still required

Source-level pins do not prove Apple's package is available or installs unattended on the exact restore image. The protected rehearsal must demonstrate clean guest discovery, accepted license handling, package receipt and developer-directory readback, Python and Git probes, and successful downstream guest workflow steps. If the pinned label or receipt differs, or installation requests interaction/restart or times out, the create fails and the executor must retire the guest before another signed create.

## PF readback and failed-create recovery

The guest helper validates the root PF configuration before installation and
then reads the loaded filter rules back with `pfctl -sr`. Apple translation
anchors (`scrub-anchor`, `nat-anchor`, `rdr-anchor`, and `dummynet-anchor`)
may appear before the first filter rule; they are not filter rules and must not
make the M5 filter-anchor check fail. The helper ignores only those four
recognized translation-anchor forms during readback. Any other rule before
`com.norn.m5.historical` remains a hard failure.

A failed Packer create can run cleanup and leave Tart inventory empty while
the durable create marker still records an ambiguous outcome. Do not infer
success or start another create from the Packer exit status or empty inventory
alone. Use the protected reconciliation and retirement workflows to bind the
failed attempt to its marker, prove the exact guest absent, and retain the
retirement receipt before issuing a fresh run-bound signed intent. Packer logs
can contain bootstrap credentials and must not be copied into chat or routine
diagnostics; report the specific sanitized failure classification instead.
