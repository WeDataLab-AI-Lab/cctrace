# Contributing to cctrace

Contributions are welcome. Read this first, because this repository merges
pull requests differently from most — the difference is structural, not a
policy anyone can waive for your PR.

## How this repository relates to the one we develop in

`cctrace` is developed in a private repository that also carries our own
deployment: host addresses, release scripts, operational runbooks. This
repository is a filtered export of the product subset — the same bytes, minus
everything that is only about running our instance.

The export is one-way and it replaces the tree wholesale. A commit made
directly here is not merged upstream by any mechanism; it is overwritten by
the next export. That is why pull requests are not merged here. It is not that
we decline to press the button — pressing it would silently undo your work a
few days later.

## What happens to your pull request

1. You open a PR here. It is reviewed here, in the open, like any other PR.
2. If it is accepted, a maintainer applies your patch upstream with your
   authorship preserved (`git am`), and it ships in the next export.
3. Your PR is closed with a link to the export commit that contains it.

Closed, not merged. The button is misleading and we would rather it stay
unpressed than have you find out from the commit graph.

### Credit

Because an export is one snapshot commit, your original commit does not appear
in this repository's history. So export commits carry a `Co-authored-by:`
trailer naming every external author whose work they contain. GitHub counts
those, so the contribution appears on your profile even though the commit
message is not yours.

If the trailer is missing or wrong on an export that contains your work, open
an issue — that is a bug, not a formality.

## Before you write code

**Open an issue first for anything non-trivial.** Not for process reasons: the
export scope is narrower than this repository suggests. Some paths you might
naturally want to change do not exist upstream in the form you see them, and
some do not exist here at all. An issue lets us tell you that before you write
the patch rather than after.

Small fixes — a typo, an obvious bug, a broken link — go straight to a PR.

## Sign your commits (DCO)

Every commit needs a `Signed-off-by` line certifying you have the right to
submit it under this project's license. Git adds it for you:

```sh
git commit -s -m "your message"
```

By signing off you agree to the [Developer Certificate of
Origin](https://developercertificate.org/). Under Apache-2.0 §5, a
contribution you deliberately submit for inclusion is licensed under the same
terms as the rest of the project unless you say otherwise in the PR.

## Build and test

The README covers the build. Before opening a PR:

```sh
make lint
make test-unit
```

`make test` additionally runs the container integration tests and needs Docker.

## Reporting a vulnerability

Do not open a public issue. See [SECURITY.md](SECURITY.md).
