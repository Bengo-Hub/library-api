# Library API - Barcode & Label Printing

**Last updated:** 2026-08-01
**Module:** `internal/modules/barcode/` (`label.go`, `sheet.go`, `card.go`, `template.go`, `tspl.go`)
**Handlers:** `internal/http/handlers/copy_label.go` (spine label), `copy_labels_bulk.go` (bulk
sheet), `members.go` / `pin_auth.go` (membership/staff cards)
**Endpoints (shipped):** `GET /{tenant}/library/catalog/copies/{id}/label.pdf`,
`POST /{tenant}/library/catalog/copies/labels/print`

This is a reference doc for whoever next extends label printing: real-world printer/label
conventions this module is built against, what's actually shipped today vs. in progress, and
why the label layout looks the way it does. It intentionally mirrors
`inventory-api/docs/barcode-labels.md` — this module was built to match that one's grid presets
and cut-guide convention so a library that also runs inventory-api gets a consistent printing
story across both services.

**Origin of the rotated-label bug this doc's TSPL/template sections fix**: the bulk endpoint
(`PrintCopyLabels`) always hardcodes its response filename to `copy-labels.pdf` — this is the
literal file a librarian downloads and then has to manually print via a Windows paper-preset
guess. Before this pass, this module had **no thermal-native output at all**: `RenderSheet` only
ever produced an Avery-grid PDF meant for a cut-sheet office printer, so printing to a thermal
roll printer (e.g. an Xprinter XP-330B) meant force-fitting that A4/Letter-shaped PDF onto a
thermal roll via a guessed Windows paper preset — exactly the failure mode that produces
rotated/misaligned output. `format: "thermal_tspl"` (below) exists so that mis-use is no longer
necessary.

---

## Table of Contents

1. [Printer types](#printer-types)
2. [Label sizes, rows/lanes, and DPI](#label-sizes-rowslanes-and-dpi)
3. [Rotation](#rotation)
4. [TSPL support (Xprinter/TSC-compatible printers)](#tspl-support-xprintertsc-compatible-printers)
5. [Direct USB printing via the local print-agent](#direct-usb-printing-via-the-local-print-agent)
6. [Symbology](#symbology)
7. [GS1-128 sizing rules](#gs1-128-sizing-rules)
8. [What this codebase supports today](#what-this-codebase-supports-today)
9. [Design decision: one card, not sections](#design-decision-one-card-not-sections)
10. [Design decision: dashed cut guide](#design-decision-dashed-cut-guide)
11. [Scope: local copy, not shared with inventory-api](#scope-local-copy-not-shared-with-inventory-api)

---

## Printer types

- **Thermal desktop label printers** (Zebra, TSC, Brother QL, DYMO LabelWriter, …) print one
  label at a time from a roll, no ink/toner — direct-thermal (heat-sensitive paper, fades over
  time/heat, fine for a shelving aid that gets replaced when a copy is rebound/relabeled) or
  thermal-transfer (ribbon-printed, more durable). They auto-cut or tear off along a
  perforation after each label, so there's no "sheet" and no cut guide is relevant. The existing
  single-copy spine-label endpoint (`RenderPDF` in `label.go`) targets this category directly:
  it renders one PDF page sized to the selected `LabelTemplate`'s real physical label dimensions
  (62mm×29mm by default), on demand, for one freshly-catalogued copy at a time. `format:
  "thermal_tspl"` (both endpoints) targets this category more directly still — TSC/TSPL2 command
  text sent straight to the printer, bypassing the PDF/Windows-driver page-size negotiation
  entirely (see [TSPL support](#tspl-support-xprintertsc-compatible-printers)).
- **Pre-cut adhesive label sheets on a regular inkjet/laser printer** (the Avery convention): a
  full A4/Letter page runs through an ordinary office printer with several labels arranged in a
  grid. Real Avery-branded stock is die-cut, but it's routinely printed on plain paper instead
  (cheaper, or the exact branded SKU isn't on hand), which then needs a hand-cut step — hence
  the dashed cut guide (see [below](#design-decision-dashed-cut-guide)). `RenderSheet` in
  `sheet.go` targets this category: one PDF with many copies' holding labels laid out on an
  Avery grid, for cataloguing a batch of new copies in one pass instead of one spine label at a
  time.

## Label sizes, rows/lanes, and DPI

- **Thermal templates** (`template` field, resolved by `LabelTemplateByName` in `template.go`),
  used by both `RenderPDF` (single, `format=avery_a4`) and `RenderThermalTSPL` (single or bulk,
  `format=thermal_tspl`), all at 203dpi:

  | Preset | Rows (lanes) | One label's size | Notes |
  |---|---|---|---|
  | `1row_62x29` (default) | 1 | 62×29mm | Exact alias of the pre-existing hardcoded spine-label size — old callers with no opinion keep working unchanged. |
  | `2row_35x29` | 2 | 35×29mm each | A wider roll die-cut into 2 parallel lanes. |
  | `3row_23x29` | 3 | 23×29mm each | 3 parallel lanes. |
  | `4row_17x29` | 4 | 17×29mm each | 4 parallel lanes. |
  | `custom` | 1-4 (`custom_lanes`) | `custom_label_w_in`/`custom_label_h_in` | Explicit W/H/lanes/gaps/rotate for a real roll that doesn't match any preset above. |

  **"Rows" = lanes across the roll's width**, not the Avery sheet's grid rows below. The
  multi-row presets are **engineering estimates** fit within a ≤80mm thermal roll width (the
  Xprinter XP-330B's confirmed media width) — **not vendor-confirmed exact stock**. Measure your
  real label roll and use `template: "custom"` if it doesn't match a preset exactly; a mismatched
  `GAP`/`SIZE` height is what causes one barcode's content to bleed across several physical
  labels.

- **Bulk sheet** (`sheet` field, only used when `format=avery_a4`, the default) — reuses the
  same two Avery grid presets as inventory-api:

  | Preset | Paper | Grid | Label size | Region fit |
  |---|---|---|---|---|
  | `l7160` (default) | A4 | 3 cols × 7 rows = 21/sheet | 63.5×38.1mm | Most of the world outside North America. |
  | `5160` | US Letter | 3 cols × 10 rows = 30/sheet | 66.7×25.4mm (2-⅝"×1") | US/Canada. Same grid shared by Avery 5160/8160/5260/8460 (adhesive/finish differs, layout doesn't). |

- **DPI:** 203dpi is the standard desktop-thermal resolution and is sufficient for the Code 128
  barcodes this module prints; 300dpi would only matter for smaller text or denser codes, neither
  of which this module currently produces (there's no GS1-128 support here — see
  [Symbology](#symbology)).

## Rotation

Labels printed to a thermal roll printer used to come out rotated when the roll's physical
mount orientation didn't match this module's hardcoded assumption (the exact bug documented for
inventory-api's `RenderSingleLabelPDF` — see `inventory-service/inventory-api/docs/barcode-labels.md`).
Every `LabelTemplate` now carries an explicit `Rotate bool`. `RenderPDF` swaps the page dims
(W×H → H×W) and wraps `drawCopyLabelCell` in an `fpdf.TransformRotate(90,…)` block, centered on
the page, when `Rotate` is set; `RenderThermalTSPL` emits a `90` rotation parameter on every
`TEXT`/`BARCODE` command instead of `0`. Neither ever swaps `SIZE`/page dims based on guesswork —
`Rotate` is an explicit fact about how the roll is mounted, set via the `rotate` request param.

## TSPL support (Xprinter/TSC-compatible printers)

`RenderThermalTSPL` (`tspl.go`) emits real TSC/TSPL2 commands (`SIZE`/`GAP`/`CLS`/`TEXT`/
`BARCODE`/`PRINT`), the command language spoken by TSC-compatible desktop printers — including
the Xprinter XP-330B — confirmed via Xprinter's own TSPL-emulation spec. It mirrors
inventory-api's `renderTSPL` (same structure/rotation convention), simplified for `CopyLabel`'s
smaller field set: title, call number, and a Code 128 barcode of the copy's own barcode number —
no GS1-128, no price, no lot/serial (this module never needed them — see
[Symbology](#symbology)). Multi-row templates tile `lanes` consecutive labels side-by-side per
feed-row, then `CLS`/`PRINT` advances to the next row, identical to `RenderSheet`'s Avery-grid
tiling applied to a continuous roll instead of a fixed sheet.

## Direct USB printing via the local print-agent

`library-ui` can print directly via USB, bypassing the OS print dialog and Windows paper-preset
guessing entirely, by reusing `pos-service`'s existing local "print-agent" (the same loopback
`127.0.0.1:9330` Windows-service companion `inventory-ui` also reuses — see
`inventory-service/inventory-api/docs/barcode-labels.md`'s "Direct USB printing" section for the
full mechanism). No agent-side change was needed: `GET /printers` and `POST /print` are already
generic/CORS-open, not POS-specific. See `library-ui/src/lib/library/print-agent.ts` and the
Copies page's bulk-print dialog "Print via Local Agent" action.

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
  `copy_label.go`, wired in `router.go`) — renders one copy's spine/holding label. Query params:
  `format` (`avery_a4` default | `thermal_tspl`), `template` (preset name or `custom`), `rotate`,
  `custom_label_w_in`/`custom_label_h_in`/`custom_lanes`/`custom_gap_x_in`/`custom_gap_y_in`
  (only with `template=custom`) — resolved by `resolveTemplate` in `copy_label.go`.
  `format=avery_a4` renders via `barcode.RenderPDF` (barcode, call number, truncated title, no
  branding — a PDF sized to the resolved template); `format=thermal_tspl` renders via
  `barcode.RenderThermalTSPL` (raw TSPL text, `Content-Type: text/plain`).
- `POST /{tenant}/library/catalog/copies/labels/print` (`PrintCopyLabels` handler in
  `copy_labels_bulk.go`, wired in `router.go`) — bulk copy-label endpoint. Body:
  `{copy_ids?, bib_id?, branch_id?, status?, sheet?, format?, template?, rotate?,
  custom_label_w_in?, custom_label_h_in?, custom_lanes?, custom_gap_x_in?, custom_gap_y_in?}`
  (exactly one of `copy_ids`/`bib_id` selects the copies; `branch_id`/`status` are optional
  additional filters). `format=avery_a4` (default) renders `barcode.RenderSheet` — `sheet` picks
  `AveryL7160` (default) or `Avery5160` via `AverySpecByName`, response filename
  `copy-labels.pdf` (the file this endpoint's rotated-label bug was originally reported
  against). `format=thermal_tspl` renders `barcode.RenderThermalTSPL` over the same selected
  copies instead — raw TSPL text, response filename `copy-labels.tspl`. Capped at 500 labels per
  request either way (400 `too_many_labels` beyond that), gated at the same `view("copies")`
  permission level as the single-label endpoint. `library-ui`'s Copies page calls this from a
  "Print labels" action that reuses the currently-applied status/branch filters as the
  selection, with a format/template picker and a "Print via Local Agent" action for
  `thermal_tspl` (see [Direct USB printing](#direct-usb-printing-via-the-local-print-agent)).
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

## Scope: local copy, not shared with inventory-api

`template.go`/`tspl.go` are a local mirror of inventory-api's same-named files (same field
shapes/semantics), not a shared Go package — kept lean for `CopyLabel`'s smaller needs (no
GS1-128/lot/serial/price, no ZPL). See
`inventory-service/inventory-api/docs/barcode-labels.md`'s "Scope decision" section for the full
reasoning (pinned-tag overhead onboarding two already-deployed services vs. the still-small
amount of genuinely-shared code) and the future-extraction path if the two modules' needs ever
converge further.
