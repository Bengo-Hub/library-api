# Library API - Barcode & Label Printing

**Last updated:** 2026-07-31
**Module:** `internal/modules/barcode/` (`label.go`, `sheet.go`, `card.go`)
**Handlers:** `internal/http/handlers/copy_label.go` (spine label), `members.go` / `pin_auth.go` (membership/staff cards)
**Endpoint (shipped):** `GET /{tenant}/library/catalog/copies/{id}/label.pdf`

This is a reference doc for whoever next extends label printing: real-world printer/label
conventions this module is built against, what's actually shipped today vs. in progress, and
why the label layout looks the way it does. It intentionally mirrors
`inventory-api/docs/barcode-labels.md` — this module was built to match that one's grid presets
and cut-guide convention so a library that also runs inventory-api gets a consistent printing
story across both services.

---

## Table of Contents

1. [Printer types](#printer-types)
2. [Label sizes and DPI](#label-sizes-and-dpi)
3. [Symbology](#symbology)
4. [GS1-128 sizing rules](#gs1-128-sizing-rules)
5. [What this codebase supports today](#what-this-codebase-supports-today)
6. [Design decision: one card, not sections](#design-decision-one-card-not-sections)
7. [Design decision: dashed cut guide](#design-decision-dashed-cut-guide)

---

## Printer types

- **Thermal desktop label printers** (Zebra, TSC, Brother QL, DYMO LabelWriter, …) print one
  label at a time from a roll, no ink/toner — direct-thermal (heat-sensitive paper, fades over
  time/heat, fine for a shelving aid that gets replaced when a copy is rebound/relabeled) or
  thermal-transfer (ribbon-printed, more durable). They auto-cut or tear off along a
  perforation after each label, so there's no "sheet" and no cut guide is relevant. The existing
  single-copy spine-label endpoint (`RenderPDF` in `label.go`) targets this category directly:
  it renders one 62mm×29mm PDF page sized to feed a single thermal label, on demand, for one
  freshly-catalogued copy at a time.
- **Pre-cut adhesive label sheets on a regular inkjet/laser printer** (the Avery convention): a
  full A4/Letter page runs through an ordinary office printer with several labels arranged in a
  grid. Real Avery-branded stock is die-cut, but it's routinely printed on plain paper instead
  (cheaper, or the exact branded SKU isn't on hand), which then needs a hand-cut step — hence
  the dashed cut guide (see [below](#design-decision-dashed-cut-guide)). `RenderSheet` in
  `sheet.go` targets this category: one PDF with many copies' holding labels laid out on an
  Avery grid, for cataloguing a batch of new copies in one pass instead of one spine label at a
  time.

## Label sizes and DPI

- **Spine label (shipped):** fixed at **62mm × 29mm** — a small direct-thermal label meant to
  feed one at a time from a desktop thermal printer. It deliberately carries **no
  company/tenant branding**: real library spine/holding labels are an internal shelving aid
  used by staff and patrons browsing the stacks, not a customer-facing branded label, and
  62×29mm is too small to spare space for a header even if branding were wanted.
- **Bulk sheet (`RenderSheet`, see [below](#what-this-codebase-supports-today)):** reuses the
  same two Avery grid presets as inventory-api:

  | Preset | Paper | Grid | Label size | Region fit |
  |---|---|---|---|---|
  | `l7160` (default) | A4 | 3 cols × 7 rows = 21/sheet | 63.5×38.1mm | Most of the world outside North America. |
  | `5160` | US Letter | 3 cols × 10 rows = 30/sheet | 66.7×25.4mm (2-⅝"×1") | US/Canada. Same grid shared by Avery 5160/8160/5260/8460 (adhesive/finish differs, layout doesn't). |

- **DPI:** the spine label's barcode is rendered as a PNG scaled to 480×120px and embedded at
  54mm×12mm on the page — there's no explicit thermal-printer DPI knob in this module (unlike
  inventory-api's `ThermalSpec`, which parameterizes 203dpi explicitly). In practice 203dpi is
  the standard desktop-thermal resolution and is sufficient for the Code 128 barcodes this
  module prints; 300dpi would only matter for smaller text or denser codes, neither of which
  this module currently produces (there's no GS1-128 support here — see
  [Symbology](#symbology)).

## Symbology

Both the spine label and the bulk sheet use **Code 128** only (`code128.Encode`, straight —
no GS1 Application Identifiers). `card.go`'s membership/staff card also encodes the member/staff
number as Code 128. There is no GS1-128, EAN-13, or lot/serial/expiry support anywhere in this
module; a library barcode is just the copy's own barcode number, which is exactly what circulation
scanning needs (no batch/expiry concept applies to a book copy the way it does to inventory
stock).

## GS1-128 sizing rules

Not applicable today — this module has no GS1-128 code path (see [Symbology](#symbology)
above). If a future feature needs denser/structured codes (e.g. encoding accession number +
condition code together), the physical constraints to design against are the same ones
inventory-api documents for its GS1-128 labels: a minimum 10x quiet zone (x = the narrow-bar
module width), a max barcode height around 33.75mm, and a minimum X-dimension on the order of
0.25mm below which scanners struggle. See
`inventory-service/inventory-api/docs/barcode-labels.md#gs1-128-sizing-rules` for the fuller
discussion, including that inventory-api's own rendering code doesn't yet enforce these
computationally either — anyone adding GS1-128 here should not assume the sibling service solved
that already.

## What this codebase supports today

**Shipped and routed:**

- `GET /{tenant}/library/catalog/copies/{id}/label.pdf` (`CopyLabel` handler in
  `copy_label.go`, wired in `router.go`) — renders one copy's spine/holding label PDF via
  `barcode.RenderPDF`: barcode, call number, truncated title, no branding. Meant for a
  direct-thermal desktop printer feeding/cutting one label at a time.
- `POST /{tenant}/library/catalog/copies/labels/print` (`PrintCopyLabels` handler in
  `copy_labels_bulk.go`, wired in `router.go`) — bulk copy-label-sheet endpoint. Body:
  `{copy_ids?: string[], bib_id?: string, branch_id?: string, status?: string, sheet?: string}`
  (exactly one of `copy_ids`/`bib_id` selects the copies; `branch_id`/`status` are optional
  additional filters; `sheet` picks `AveryL7160` (default) or `Avery5160` via
  `AverySpecByName`). Capped at 500 labels per request (400 `too_many_labels` beyond that).
  Renders via `barcode.RenderSheet`, gated at the same `view("copies")` permission level as
  the single-label endpoint. `library-ui`'s Copies page calls this from a "Print labels"
  action that reuses the currently-applied status/branch filters as the selection.
- Membership/staff ID cards (`RenderMemberCard` in `card.go`, called from `members.go` and
  `pin_auth.go`) — a CR80-sized (85.6mm×54mm) card with a branded header band, photo/silhouette,
  and a Code 128 barcode of the membership/staff number. Not a shelf label; documented here only
  because it lives in the same package.

`barcode.RenderSheet` (`sheet.go`) takes a slice of `CopyLabel` plus an `AverySpec`
(`AveryL7160()` / `Avery5160()` / `AverySpecByName`) and renders the Avery-grid PDF sheet,
mirroring inventory-api's `renderAveryPDF`/`drawCutGuide`/`drawLabelCell` pattern (duplicated
rather than shared, since this is a separate Go module with its own `internal/modules/barcode`
package).

## Design decision: one card, not sections

Every label cell on the Avery sheet (`drawCopyLabelCell` in `sheet.go`) renders as **one
stacked column of rows**: tenant/library name, title, call number, barcode image,
human-readable barcode text — no colored multi-section banner. The single spine label
(`RenderPDF` in `label.go`) follows the same "stacked rows, no banner" spirit but omits the
tenant-name row entirely (see [Label sizes and DPI](#label-sizes-and-dpi) — that's a
deliberate omission, not an oversight). Same reasoning as inventory-api for the row-stack
approach itself:

- **Space.** These are small labels (spine label: 62×29mm; sheet cells: as small as
  25.4mm/1" tall on the `5160` preset). A colored header band competes directly with the room
  the barcode's quiet zone and human-readable text need.
- **Print cost at scale.** The bulk sheet exists precisely to print many copies' labels in one
  run during cataloguing; a solid colored band burns toner across every one of them for no
  functional benefit.
- **It matches how real library spine labels work.** They're an internal shelving aid for staff
  and patrons browsing shelves, not a customer-facing branded artifact — a plain, information-
  dense stack of rows is the convention, not a decorated card.

## Design decision: dashed cut guide

`drawCutGuide` in `sheet.go` draws the same dashed (not solid) rectangle around each cell that
inventory-api uses:

- **Why a guide at all**: bulk sheets are commonly printed on plain, non-perforated paper rather
  than genuine die-cut Avery stock, so there's otherwise no physical cue for where to cut by
  hand.
- **Why dashed, not solid**: a solid border reads as part of the label's own design and is
  redundant/wrong-looking on stock that's already die-cut or slightly misaligned in the printer.
  A dashed line reads unambiguously as a cut/perforation guide (the same visual convention as
  ticket stubs or tear-off coupons), never as label content.
