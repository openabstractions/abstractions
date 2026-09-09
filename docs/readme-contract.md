# What a README in this organisation must be

Binding on every repository. An external reviewer read our documentation cold
and reported that it was a manifesto rather than documentation: arguments with
imagined critics, war stories about our own past bugs, aphorisms written to be
quoted, and proof links that 404. This file exists so that does not recur.

## Required structure, in this order

1. **One sentence saying what this is.** Not what it is like, not what it
   replaces, not a story. A stranger must be able to repeat it back.
2. **One paragraph on the problem it solves**, concrete and checkable.
3. **Install.** The literal commands. Include how to *obtain* the code.
4. **A minimal example that runs.** Every identifier defined, every import
   shown. Someone must be able to paste it and see it work.
5. **API overview.** Every public entry point named. If a method has unusual
   semantics, say so here.
6. **Status.** Experimental or stable. What may break. What is unfinished.
7. **Requirements.** Language versions, dependencies, platforms.
8. **Licence.**

## Banned

- Any sentence whose purpose is to show a decision was clever.
- "That is the point", "which is the whole", "the thing that does the work",
  "deliberately", dramatic reveals, and the aphorism-with-a-colon construction.
- Arguing with an objection nobody made.
- Referring to earlier drafts, previous versions of the file, or decisions the
  reader did not witness. Version control is the changelog.
- War stories about our own bugs. Byte counts from incidents a stranger cannot
  verify. "A real 397 MB file" means nothing to them.
- Universal claims about other projects without a citation: *"every tool loses
  a 40 GB model"* is unsourced and false — several resume correctly.
- Negative benchmarks of named products without a published method.
- Citing our own unpublished notes as authority.
- Instructions addressed to a maintainer. There is no maintainer but us.
- Transcripts containing usernames, machine names, or local paths.

## Required of every claim

- **"Proven" needs a link a stranger can follow or a command they can run.** A
  pasted terminal transcript authored by us is an assertion, not evidence.
- **Every link must resolve.** Repositories were split; most cross-references
  are now broken. Check every one.
- **Every term defined on first use** or linked. Words that currently arrive
  undefined: binding, kind, orphan, adoption, delegation, tier, supervisor,
  epoch, checkpoint, lease, facade, sink, spec, artifact, digest.
- **Test counts, file counts and version numbers must be recounted**, not
  copied. Several are wrong.

## Length

Under 200 lines. If the design reasoning matters, it belongs in a linked design
document, not the front door.

## Repository hygiene

- `LICENSE` present. **Apache-2.0** across the organisation: permissive, with a
  patent grant, and it matches the first upstream project we intend to
  contribute to.
- `.gitignore` covering `__pycache__/`, `*.pyc`, build directories.
- No compiled artefacts committed. Several repositories currently ship `.pyc`.
- No file that only made sense inside the old single repository.
