---
name: release-highlights
description: Write the highlights of a published CONSTRUCT release - the few changes a person notices, in plain words - above the notes the builder generated, and update the GitHub Release. Use after task release has published vYYYYMMDD.N, or when asked to describe, announce or summarize a release.
---

# Release highlights

The builder publishes every release with notes written from data (`cmd/image/notes.go`): the
commits by area, the packages that moved, Grype's findings, the harness's results. They are
complete and exact, and no person reads them first. The highlights are what a person reads
first: what is different for someone who uses the machine, in a few lines.

## Steps

1. Find the release: the tag the person names, or the newest one.

   ```sh
   gh release view <tag> --json tagName,body,publishedAt,url
   gh release list --limit 2 --json tagName      # the previous one, to compare against
   ```

2. Read what changed, from the sources, not from memory:
   - the generated notes (the release body): Changes by area, Packages, Security, Checked;
   - the commits themselves for anything whose subject is not enough:
     `git log --no-merges <previous>..<tag>`, and `git show <sha>` for the body, which says why;
   - `packages.txt` of both releases for the versions a person knows by name (GNOME, the
     kernel, Mesa, Firefox, VS Code).

3. Choose three to six highlights a person would notice or should know:
   - something they can see or do differently (the desktop's look, an app, a setting, hardware
     that now works);
   - a security fix with a CVE a version fixed, when it is Critical or High;
   - a major version of something they use (GNOME, the kernel, Mesa);
   - something that needs them to act (a reboot is never one: every update asks for it).

   Leave out what only the build or the harness sees (tests, CI, refactors, recipes' internals),
   unless it changed what the machine does.

4. Write them in the repository's voice: English, plain, short sentences, each saying what changed
   and why it matters to the person, no marketing words ("exciting", "powerful", "seamless"), no
   emoji. Name versions exactly as packages.txt has them. Nothing that the sources do not say.

   ```markdown
   ## Highlights

   - **Files' headerbar is shorter**, 44 px with 32 px controls, like the rest of GNOME's.
   - **GNOME Shell 51.1** fixes ... (from packages.txt and its NEWS, when a commit cites it).
   - **Security:** OpenSSL 3.5.1 fixes CVE-2026-1234 (High).
   ```

5. Put the highlights above the generated notes, after the title and the build line, and update
   the release. Never edit or remove the generated sections: they are the record.

   ```sh
   gh release view <tag> --json body --jq .body > /tmp/notes.md
   # insert the Highlights section after the line that starts with "Built "
   gh release edit <tag> --notes-file /tmp/notes.md
   ```

6. Show the person the highlights and the release's URL. If they asked for an announcement
   (a post, a message), write it from the highlights alone, and give it to them to post: never
   post anywhere yourself.
