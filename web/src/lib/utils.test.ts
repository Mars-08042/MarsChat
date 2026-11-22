import { describe, expect, it } from "vitest"
import { formatBytes } from "./utils"

const MiB = 1024 * 1024

describe("formatBytes", () => {
  it.each([
    [0, "0 B"],
    [1023, "1023 B"],
    [1024, "1.0 KB"],
    [1536, "1.5 KB"],
    [20 * 1024, "20 KB"],
    [3.4 * MiB, "3.4 MB"],
    [100 * MiB, "100 MB"],
    [1024 * MiB, "1.0 GB"],
  ])("%d → %s", (bytes, want) => {
    expect(formatBytes(bytes)).toBe(want)
  })
})
