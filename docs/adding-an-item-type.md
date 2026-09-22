# Adding an item type

The archive's value is breadth: photos, two-sided photos, stacks, documents,
CDs, and whatever you want to catalogue next. So adding a type is deliberately
a **data change, not a code change** — a YAML file in
`internal/domain/itemtypes/manifests/`, and usually nothing else.

There is no database migration. There is no new table, no new index, no change
to any endpoint, and no change to how the gallery, map, or timeline query. If
you find yourself editing storage or query code to add a type, something has
gone wrong — come back to this document.

## The model

An item splits into three parts, and only the third varies by type:

1. **Common archival metadata** — `title`, `date`, `location`, `notes`,
   `tags`, `people`. Universal and strongly typed. This is what Gallery, Map,
   and Timeline read, and it's why a CD and a photograph can sit next to each
   other on one timeline.
2. **Files**, each filling a named **slot** the type declares.
3. **Attributes** — a small map of type-specific fields.

Because only the *vocabulary* of slots and attributes varies, the stored shape
of an item never changes. That's the whole trick.

### What does *not* belong in a type

Anything you'd want to filter, sort, or browse the entire archive by belongs in
common metadata, not in `attributes`. A date is common metadata even though a
CD's release year feels type-specific — if you put the year in `attributes`,
the CD silently vanishes from the timeline. Ask: *would I ever want this on the
timeline, the map, or a filter chip?* If yes, it isn't an attribute.

## Worked example: a letter

A letter is an envelope (front and back) plus pages. Every part of it is
already expressible, so this costs one file and no code.

`internal/domain/itemtypes/manifests/letter.yaml`:

```yaml
id: letter
version: 1
label: Letter
icon: mail
category: documents

slots:
  - id: envelope-front
    label: Envelope front
    accepts: [image]
    max: 1

  - id: envelope-back
    label: Envelope back
    accepts: [image]
    max: 1

  - id: page
    label: Pages
    accepts: [image, pdf]
    max: 50
    ordered: true

  - id: voice-memo
    label: Voice memos
    accepts: [audio]
    max: 20
    ordered: true
    maxSizeBytes: 104857600

cover:
  prefer: [envelope-front, page]

presentation:
  - { primitive: flippable,  slots: [envelope-front, envelope-back] }
  - { primitive: paged,      slots: [page] }
  - { primitive: audio,      slots: [voice-memo] }
```

That's the whole change. The envelope reuses the same flip card as a two-sided
photo; the pages reuse the same book spread as a document.

### Slots are optional by default

`min` defaults to `0`, so every slot above is optional. **This is the normal
case, not a concession.** A letter with pages but no envelope is a letter. A CD
with only an MP3 is a CD. Requiring a file (`min: 1`) is the exception, and you
should have a specific reason.

`max` is required and must be at least 1. There is no "leave it blank for
unlimited" — pick a real ceiling. (An `int` whose zero value means something
is exactly how `order: 0` became indistinguishable from "no order" in the old
model.)

## The primitive catalogue

The UI renders by **capability, not by type name** — it has no
`switch (type)`. It holds a handful of primitives, and your type composes them:

| Primitive | Shows | Slot shape it expects |
| --- | --- | --- |
| `single-image` | one image | exactly 1 slot, `max: 1` |
| `flippable` | two faces with a flip animation | exactly 2 slots, both `max: 1` |
| `paged` | an ordered sequence as a book spread | 1+ slots |
| `stack` | a loose pile you riffle through | 1+ slots |
| `audio` | waveform player | 1+ slots |
| `pdf` | PDF page renderer | 1+ slots |
| `video` | player with a poster frame | exactly 1 slot, `max: 1` |
| `model3d` | interactive 3D viewer | exactly 1 slot, `max: 1` |

**If your type composes existing primitives, you write no UI code at all.**
That's true of the letter above, and it was true of the CD — which is four
primitives the UI already had.

### When you *do* need a new primitive

Only when the thing genuinely cannot be shown any existing way. A 3D scan is
the clear case: nothing in the list rotates a mesh. Then it's three steps
instead of one:

1. Add the primitive constant in `internal/domain/itemtypes/types.go` and to
   `knownPrimitives`.
2. Add any structural rule it needs to `validatePresentation` (e.g. "exactly
   one slot").
3. Build the component in `archives-ui` and register it in the primitive map.

Adding a *media family* (say `model3d` bytes that need a poster render) also
means a new `Deriver` in `internal/media/derive/` — one implementation,
registered in a map, touching nothing else.

Resist inventing a primitive that's a near-copy of an existing one. Two
primitives that differ only in styling is how a `switch (type)` grows back.

## Compatibility rules

An archive is forever. Someone will open a record in 2050 that was written
today, with a client built in between. So:

- **Adding an optional slot or attribute is always safe.** Old items just have
  nothing in it. No version bump needed.
- **Never delete a slot.** Mark it `deprecated: true`. It disappears from
  capture UI but still renders files already stored in it — keep it in
  `presentation`, which the validator enforces.
- **Renaming a slot** uses `renamedFrom: [old-id]` so existing files still
  resolve. Point `presentation` at the *new* id; the validator rejects naming
  an alias there, so the manifest can't drift into ambiguity.
- **Narrowing is breaking.** Lowering a `max`, adding a `min`, or dropping an
  accepted family needs a `version` bump and a migration plan. Expect this to
  be rare.
- **An unknown type renders generically.** A client that doesn't recognise a
  `typeId` falls back to showing common metadata plus each file by media
  family. Older clients degrade; they don't break.

## What the validator catches for you

The registry loads at startup and **panics on a bad manifest** — a broken
catalogue would validate uploads against the wrong rules, so failing to boot
beats serving a subtly wrong contract. It rejects:

- unknown YAML keys (a typo'd `slotz:` fails loudly instead of doing nothing)
- unknown media families or primitives
- `max` below 1, or `min` above `max`
- a `cover.prefer` slot that holds no image or PDF — otherwise the gallery tile
  is blank with no obvious cause
- a slot nothing renders, or a slot rendered twice
- `presentation` naming an unknown slot, or an alias instead of the real id
- structural mismatches, e.g. `flippable` over one slot, or `single-image`
  over a slot that allows many files
- duplicate type, slot, alias, or attribute ids
- an `enum` attribute with no `options`

## Checklist

1. Write the manifest in `internal/domain/itemtypes/manifests/<id>.yaml`.
2. `go test ./internal/domain/itemtypes/` — `TestDefaultRegistry` runs your
   manifest through the real validator.
3. `make generate` — regenerates the Go enums and the TypeScript union and zod
   schemas the UI consumes.
4. Only if it needs a new primitive: add the constant, its structural rule, and
   the UI component.
5. Add a fixture and a round-trip test asserting cover selection and capability
   derivation for a realistic *partial* set of slots — the one with holes in
   it, since that's the normal case.
6. Ship. No migration, no backfill.

## Where things live

| Path | What |
| --- | --- |
| `internal/domain/itemtypes/manifests/*.yaml` | the catalogue |
| `internal/domain/itemtypes/types.go` | slot/primitive/family model |
| `internal/domain/itemtypes/registry.go` | loading and validation |
| `GET /v1/item-types` | the catalogue, served to the UI, ETag'd |
| `infra/terraform/dynamodb.tf` | the index — note it projects only *common* metadata, which is why types never touch it |
