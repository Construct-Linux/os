# Security

CONSTRUCT boots through Secure Boot into a signed unified kernel image, checks its root block by
block with dm-verity, and takes updates only when the release key signs them. A flaw in any of that
is what this file is for.

## Report a vulnerability

Privately, through GitHub: the repository's **Security** tab, **Report a vulnerability**. Not in
an issue, a pull request or a discussion, which everyone can read.

Say what it is, which release (`cat /usr/lib/os-release`) or commit, and how to reproduce it. A
proof of concept helps; you need not have one.

You will hear back within a week. The fix is made in the advisory's private fork, released, and
the advisory published with it, crediting you unless you would rather not be named.

## What counts

- the boot chain: the Secure Boot keys and their enrollment, the unified kernel image, the
  initrd, measured boot and the TPM2 policy;
- the root's integrity: dm-verity and the read-only image;
- updates: the release signatures, the channel indexes, systemd-sysupdate's configuration, the
  rollback after failed boots;
- `construct install` and the installer: disk encryption, the first person and their password;
- the kernel's configuration and the patches in `kernel/patches/`;
- what the recipes in `recipes/` build, and how the image puts it together.

## What does not

- a known CVE in a package: the release's Grype scan finds it, and `task watch` opens a public
  issue (label `security`) for each Critical or High that a newer version fixes;
- a flaw in Wolfi, GNOME, the kernel or another upstream that CONSTRUCT ships as it is: report it
  to that project; tell us too if it is serious, and we take its fix early;
- a machine whose owner turned Secure Boot off or enrolled their own keys.

## Versions

Only the newest release in each channel (`stable`, `testing`) gets fixes. An update is the way
to take one: there are no backports.
