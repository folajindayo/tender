# Counting eval set

Photographs of real naira, hand-labelled, used to measure how well a model
actually counts. **Model choice for this task should come from these numbers,
not from intuition about model tiers** — counting banknotes is perception, and a
cheap model may well match an expensive one at a twentieth of the price.

## Adding cases

Drop the photograph in this folder and add a row to `manifest.json`:

```json
{
  "images": [
    { "file": "market-01.jpg", "counts": { "1000": 20 } },
    { "file": "mixed-02.jpg",  "counts": { "500": 8, "200": 5, "100": 3 } },
    { "file": "screen-01.jpg", "counts": {}, "screenReplay": true,
      "note": "photographed off a laptop screen" },
    { "file": "photocopy-01.jpg", "counts": { "1000": 5 }, "photocopy": true }
  ]
}
```

`counts` is keyed by denomination in naira. Count by hand and be exact — the
labels are the ground truth, so a sloppy label reads as a model error.

## What to photograph

Aim for 60–100 cases that look like the conditions this will really run in, not
studio shots:

- **Layouts** — neat rows, a loose fan, a partial overlap, a single stack
- **Denominations** — one denomination alone, and realistic mixtures
- **Both note designs** — the ₦200/₦500/₦1000 redesign and the older notes are
  both circulating, and a model may be less sure about one of them
- **Condition** — crisp notes and the soft, worn, taped ones that are normal in a market
- **Light** — daylight, indoor bulb, phone torch, harsh shade, slight motion blur
- **Angles** — flat overhead, and the tilted angle people actually shoot at
- **Negative cases** — cash on a screen, a photocopy, a photo with no cash in it

Include the awkward ones. A set of clean overhead shots will tell you the system
works and then it will fail in a market.

## Running it

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./cmd/eval -manifest testdata/naira/manifest.json \
    -models claude-haiku-4-5,claude-sonnet-5,claude-opus-5
```

The summary reports, per model: the exact-total pass rate (the number that
matters — a miscount blocks the transfer), mean notes miscounted, serial
legibility, screen-replay detection with false positives, median latency, and
measured cost per snap against the fee it has to come out of.

Photographs of real money are not committed to the repository; keep them local
or in private storage.
