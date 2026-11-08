# Release notes

One file per stable tag, holding the GitHub Release body verbatim. Publish with
`gh release edit vX.Y.Z --notes-file docs/releases/vX.Y.Z.md`.

These files target GitHub's renderer rather than the docs site, which excludes
this directory. A single newline renders as a line break there, so paragraphs
are not wrapped.

Image and chart digests exist only after the tag is built, so they are added
afterwards. A tagged copy of a notes file can therefore be one section behind the
published body. The file on `main` is the one that must match.
