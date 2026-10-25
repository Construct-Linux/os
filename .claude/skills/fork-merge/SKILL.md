---
name: fork-merge
description: Bring a Construct-Linux fork (Orchis, Tela, Bibata, Vitals, Dash to Panel, Tiling Shell, AppIndicator, Caffeine, Clipboard Indicator) up to its upstream and move its recipe's pin, resolving what task fork:sync could not. Use when asked to update, sync or rebase a fork, port or review upstream's changes, resolve a fork's merge, or when a watch issue says "update: <fork> is N commit(s) behind".
---

# Fork merge

A fork is upstream plus the fixes triaged against this stack, one commit each, on the branch
the recipe names (`gnome-51`). `cmd/fork` does what needs no judgement: `sync` merges upstream,
`check` says whether the tree holds, `pin` moves the recipe. This skill is the rest: deciding a
conflict, deciding what of upstream's the desktop should not take, and saying why in words.

The checkouts sit beside this repository, one per repository name (`../Vitals`), with `upstream`
as a remote. Nothing here pushes: the recipe clones the fork from GitHub, so the person pushes
`gnome-51` before `task build` can run.

## Steps

1. **Sync.** `task fork:sync -- <recipe>` (the recipe's name, `gnome-shell-extension-vitals`,
   or the fork's, `Vitals`). Read `_output/fork/<fork>/report.md`: upstream's commits since the
   base, the fork's own, which are identical (same patch on both sides), which touch the same
   files, and the conflicts.

2. **Judge the result.**
   - `at-head`: nothing to do.
   - `merged`: read what came in before trusting it: `git -C ../<fork> show --stat HEAD` and the
     diff of each file the merge changed. Upstream can bring what the fork removed on purpose
     (see 3). Then go to 5.
   - `conflicts`: resolve them (3), then 4.
   - The report says upstream's commits add nothing on the paths the fork never touched while
     conflicts are everywhere: upstream released the branch the fork was cut from, squashed.
     Confirm with `git -C ../<fork> diff HEAD upstream/<branch> -- <the fork's own paths>`; when
     the delta is already in the fork, `git -C ../<fork> merge --abort` and
     `task fork:sync -- <recipe> -ours`, then 5.

3. **Resolve, by these rules.** What the fork keeps is written down: the fork's README
   ("Changes from upstream") and the comment above `git-checkout` in the recipe. Read both first.
   - An **identical** commit on both sides needs nothing: git merges it.
   - **Both fixed the same thing** differently: take upstream's and let the fork's go, unless the
     fork's is right for a 51-only extension or does less work at rest (fewer reads, fewer
     signals); then keep the fork's and say why in the merge's message.
   - **Files the fork deleted** stay deleted: upstream's CI, templates, release tooling, browser
     add-ons, the builds for older shells. `git rm` them. `fork check` notes any that came back.
   - **metadata.json**: `shell-version` is `["51"]` alone, `url` is the fork's, no `version`,
     `donations` or `version-name` unless the fork kept them.
   - **The README** is the fork's.
   - A **preference for a feature the fork removed** goes with it: the rows in `prefs.ui` or
     `prefs.js`, the schema key and the settings list in `extension.js`, together.
   - A **feature upstream added** is taken when it serves the person on this desktop and costs
     nothing at rest; otherwise reverted on the branch in a commit of its own that says why.
   - Resolve only the conflicts and what they imply. Do not reformat, rename or tidy.

   After editing: `git -C ../<fork> add` each file, `task fork:check -- <recipe>`, fix what it
   lists, repeat.

4. **Commit the merge** in the fork: subject `Merge upstream <branch> into gnome-51`; the body
   says what the merge brings for the person (versions, fixes, features, in plain words), then
   each resolution and its reason. English.

5. **Pin.** `task fork:pin -- <recipe>` moves `expected-commit` and the version or epoch. Then
   write the comment above `git-checkout` in the recipe anew: what upstream version this is, and
   what the fork carries over it, each with its upstream issue number when there is one. The
   recipe's voice is the repository's: why, not what.

6. **Check.** `task check`.

7. **Stop and say what is left to the person**: the fork's `gnome-51` is not pushed, and the
   recipe builds from GitHub. Give the command (`git -C ../<fork> push origin gnome-51`) and the
   gates that follow it, which this skill does not run on its own: `task build -- <recipe>`,
   `task lock`, `task disk -- -test && go run ./cmd/image check`, `task test` reading
   `shell-startup.txt` and `idle.txt` against the last build's, and the screenshots in
   `_output/test` when a stylesheet or an icon changed.

## Reviewing what upstream has open

When asked what upstream has that the fork should take before it is merged (open issues and pull
requests), read them from the source:

```sh
gh issue list -R <owner/repo> --state open --limit 100 --json number,title,labels,updatedAt
gh pr list -R <owner/repo> --state open --limit 100 --json number,title,files,updatedAt
```

Keep what fixes a bug the desktop can hit or improves what the person sees or does; leave out
what GNOME already does, what serves other desktops or shells older than 51, and what costs size,
privacy or hardware reports. Record what was left out and why in the fork's README under
"Changes from upstream", so it is not proposed again.
