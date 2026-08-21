const PAIRING_KEYS = [
  "version",
  "base_url",
  "host_id",
  "grant_id",
  "pairing_secret",
  "expires_at",
] as const;

const EXP = new Uint8Array(512);
const LOG = new Uint8Array(256);
(() => {
  let value = 1;
  for (let index = 0; index < 255; index += 1) {
    EXP[index] = value;
    LOG[value] = index;
    value <<= 1;
    if (value & 0x100) value ^= 0x11d;
  }
  for (let index = 255; index < 512; index += 1) EXP[index] = EXP[index - 255];
})();

function gfMul(left: number, right: number) {
  if (left === 0 || right === 0) return 0;
  return EXP[LOG[left] + LOG[right]];
}

type BlockLayout = {
  ecPerBlock: number;
  g1Blocks: number;
  g1Data: number;
  g2Blocks: number;
  g2Data: number;
};

// Error-correction block layout for versions 1-20, levels L then M.
const BLOCKS: BlockLayout[][] = [
  [
    { ecPerBlock: 7, g1Blocks: 1, g1Data: 19, g2Blocks: 0, g2Data: 0 },
    { ecPerBlock: 10, g1Blocks: 1, g1Data: 16, g2Blocks: 0, g2Data: 0 },
  ],
  [
    { ecPerBlock: 10, g1Blocks: 1, g1Data: 34, g2Blocks: 0, g2Data: 0 },
    { ecPerBlock: 16, g1Blocks: 1, g1Data: 28, g2Blocks: 0, g2Data: 0 },
  ],
  [
    { ecPerBlock: 15, g1Blocks: 1, g1Data: 55, g2Blocks: 0, g2Data: 0 },
    { ecPerBlock: 26, g1Blocks: 1, g1Data: 44, g2Blocks: 0, g2Data: 0 },
  ],
  [
    { ecPerBlock: 20, g1Blocks: 1, g1Data: 80, g2Blocks: 0, g2Data: 0 },
    { ecPerBlock: 18, g1Blocks: 2, g1Data: 32, g2Blocks: 0, g2Data: 0 },
  ],
  [
    { ecPerBlock: 26, g1Blocks: 1, g1Data: 108, g2Blocks: 0, g2Data: 0 },
    { ecPerBlock: 24, g1Blocks: 2, g1Data: 43, g2Blocks: 0, g2Data: 0 },
  ],
  [
    { ecPerBlock: 18, g1Blocks: 2, g1Data: 68, g2Blocks: 0, g2Data: 0 },
    { ecPerBlock: 16, g1Blocks: 4, g1Data: 27, g2Blocks: 0, g2Data: 0 },
  ],
  [
    { ecPerBlock: 20, g1Blocks: 2, g1Data: 78, g2Blocks: 0, g2Data: 0 },
    { ecPerBlock: 18, g1Blocks: 4, g1Data: 31, g2Blocks: 0, g2Data: 0 },
  ],
  [
    { ecPerBlock: 24, g1Blocks: 2, g1Data: 97, g2Blocks: 0, g2Data: 0 },
    { ecPerBlock: 22, g1Blocks: 2, g1Data: 38, g2Blocks: 2, g2Data: 39 },
  ],
  [
    { ecPerBlock: 30, g1Blocks: 2, g1Data: 116, g2Blocks: 0, g2Data: 0 },
    { ecPerBlock: 22, g1Blocks: 3, g1Data: 36, g2Blocks: 2, g2Data: 37 },
  ],
  [
    { ecPerBlock: 18, g1Blocks: 2, g1Data: 68, g2Blocks: 2, g2Data: 69 },
    { ecPerBlock: 26, g1Blocks: 4, g1Data: 43, g2Blocks: 1, g2Data: 44 },
  ],
  [
    { ecPerBlock: 20, g1Blocks: 4, g1Data: 81, g2Blocks: 0, g2Data: 0 },
    { ecPerBlock: 30, g1Blocks: 1, g1Data: 50, g2Blocks: 4, g2Data: 51 },
  ],
  [
    { ecPerBlock: 24, g1Blocks: 2, g1Data: 92, g2Blocks: 2, g2Data: 93 },
    { ecPerBlock: 22, g1Blocks: 6, g1Data: 36, g2Blocks: 2, g2Data: 37 },
  ],
  [
    { ecPerBlock: 26, g1Blocks: 4, g1Data: 107, g2Blocks: 0, g2Data: 0 },
    { ecPerBlock: 22, g1Blocks: 8, g1Data: 37, g2Blocks: 1, g2Data: 38 },
  ],
  [
    { ecPerBlock: 30, g1Blocks: 3, g1Data: 115, g2Blocks: 1, g2Data: 116 },
    { ecPerBlock: 24, g1Blocks: 4, g1Data: 40, g2Blocks: 5, g2Data: 41 },
  ],
  [
    { ecPerBlock: 22, g1Blocks: 5, g1Data: 87, g2Blocks: 1, g2Data: 88 },
    { ecPerBlock: 24, g1Blocks: 5, g1Data: 41, g2Blocks: 5, g2Data: 42 },
  ],
  [
    { ecPerBlock: 24, g1Blocks: 5, g1Data: 98, g2Blocks: 1, g2Data: 99 },
    { ecPerBlock: 28, g1Blocks: 7, g1Data: 45, g2Blocks: 3, g2Data: 46 },
  ],
  [
    { ecPerBlock: 28, g1Blocks: 1, g1Data: 107, g2Blocks: 5, g2Data: 108 },
    { ecPerBlock: 28, g1Blocks: 10, g1Data: 46, g2Blocks: 1, g2Data: 47 },
  ],
  [
    { ecPerBlock: 30, g1Blocks: 5, g1Data: 120, g2Blocks: 1, g2Data: 121 },
    { ecPerBlock: 26, g1Blocks: 9, g1Data: 43, g2Blocks: 4, g2Data: 44 },
  ],
  [
    { ecPerBlock: 28, g1Blocks: 3, g1Data: 113, g2Blocks: 4, g2Data: 114 },
    { ecPerBlock: 26, g1Blocks: 3, g1Data: 44, g2Blocks: 11, g2Data: 45 },
  ],
  [
    { ecPerBlock: 28, g1Blocks: 3, g1Data: 107, g2Blocks: 5, g2Data: 108 },
    { ecPerBlock: 26, g1Blocks: 3, g1Data: 41, g2Blocks: 13, g2Data: 42 },
  ],
];

const ALIGNMENT = [
  [],
  [6, 18],
  [6, 22],
  [6, 26],
  [6, 30],
  [6, 34],
  [6, 22, 38],
  [6, 24, 42],
  [6, 26, 46],
  [6, 28, 50],
  [6, 30, 54],
  [6, 32, 58],
  [6, 34, 62],
  [6, 26, 46, 66],
  [6, 26, 48, 70],
  [6, 26, 50, 74],
  [6, 30, 54, 78],
  [6, 30, 56, 82],
  [6, 30, 58, 86],
  [6, 34, 62, 90],
];

function dataCodewords(layout: BlockLayout) {
  return layout.g1Blocks * layout.g1Data + layout.g2Blocks * layout.g2Data;
}

function remainderBits(version: number) {
  if (version >= 2 && version <= 6) return 7;
  if (version >= 14 && version <= 20) return 3;
  return 0;
}

function generatorPoly(degree: number) {
  let poly = [1];
  for (let index = 0; index < degree; index += 1) {
    const next = new Array(poly.length + 1).fill(0);
    for (let offset = 0; offset < poly.length; offset += 1) {
      next[offset] ^= gfMul(poly[offset], EXP[index]);
      next[offset + 1] ^= poly[offset];
    }
    poly = next;
  }
  return poly;
}

function reedSolomon(data: number[], ecCount: number) {
  const generator = generatorPoly(ecCount);
  const ecc = new Array(ecCount).fill(0);
  for (const byte of data) {
    const factor = byte ^ ecc[0];
    ecc.shift();
    ecc.push(0);
    if (factor === 0) continue;
    for (let index = 0; index < ecCount; index += 1) {
      ecc[index] ^= gfMul(generator[index + 1], factor);
    }
  }
  return ecc;
}

function pushBits(bits: number[], value: number, length: number) {
  for (let index = length - 1; index >= 0; index -= 1) {
    bits.push((value >>> index) & 1);
  }
}

function bitsToBytes(bits: number[]) {
  const bytes: number[] = [];
  for (let index = 0; index < bits.length; index += 8) {
    let value = 0;
    for (let offset = 0; offset < 8; offset += 1) {
      value = (value << 1) | (bits[index + offset] ?? 0);
    }
    bytes.push(value);
  }
  return bytes;
}

function encodeData(payload: Uint8Array, version: number, layout: BlockLayout) {
  const countBits = version <= 9 ? 8 : 16;
  const capacity = dataCodewords(layout);
  const bits: number[] = [];
  pushBits(bits, 0b0100, 4);
  pushBits(bits, payload.length, countBits);
  for (const byte of payload) pushBits(bits, byte, 8);
  const capacityBits = capacity * 8;
  const terminator = Math.min(4, capacityBits - bits.length);
  pushBits(bits, 0, terminator);
  while (bits.length % 8 !== 0) bits.push(0);
  const bytes = bitsToBytes(bits);
  const pads = [0xec, 0x11];
  let pad = 0;
  while (bytes.length < capacity) {
    bytes.push(pads[pad % 2]);
    pad += 1;
  }
  return bytes.slice(0, capacity);
}

function interleave(data: number[], layout: BlockLayout) {
  const blocks: { data: number[]; ecc: number[] }[] = [];
  let offset = 0;
  const sizes = [
    ...Array.from({ length: layout.g1Blocks }, () => layout.g1Data),
    ...Array.from({ length: layout.g2Blocks }, () => layout.g2Data),
  ];
  for (const size of sizes) {
    const block = data.slice(offset, offset + size);
    offset += size;
    blocks.push({ data: block, ecc: reedSolomon(block, layout.ecPerBlock) });
  }
  const maxData = Math.max(layout.g1Data, layout.g2Data || layout.g1Data);
  const out: number[] = [];
  for (let index = 0; index < maxData; index += 1) {
    for (const block of blocks) {
      if (index < block.data.length) out.push(block.data[index]);
    }
  }
  for (let index = 0; index < layout.ecPerBlock; index += 1) {
    for (const block of blocks) out.push(block.ecc[index]);
  }
  return out;
}

function formatBits(ecl: number, mask: number) {
  const data = (ecl << 3) | mask;
  let rem = data << 10;
  for (let bit = 14; bit >= 10; bit -= 1) {
    if ((rem >>> bit) & 1) rem ^= 0b10100110111 << (bit - 10);
  }
  return ((data << 10) | rem) ^ 0b101010000010010;
}

function versionBits(version: number) {
  let rem = version << 12;
  for (let bit = 17; bit >= 12; bit -= 1) {
    if ((rem >>> bit) & 1) rem ^= 0x1f25 << (bit - 12);
  }
  return (version << 12) | rem;
}

function setModule(
  grid: Int8Array[],
  reserved: Uint8Array[],
  row: number,
  col: number,
  dark: number,
) {
  grid[row][col] = dark ? 1 : 0;
  reserved[row][col] = 1;
}

function placeFinder(
  grid: Int8Array[],
  reserved: Uint8Array[],
  row: number,
  col: number,
) {
  for (let dy = -1; dy <= 7; dy += 1) {
    for (let dx = -1; dx <= 7; dx += 1) {
      const y = row + dy;
      const x = col + dx;
      if (y < 0 || x < 0 || y >= grid.length || x >= grid.length) continue;
      const onRing =
        dx === -1 || dx === 7 || dy === -1 || dy === 7
          ? 0
          : dx === 0 || dx === 6 || dy === 0 || dy === 6
            ? 1
            : dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4
              ? 1
              : 0;
      setModule(grid, reserved, y, x, onRing);
    }
  }
}

function placeAlignment(
  grid: Int8Array[],
  reserved: Uint8Array[],
  row: number,
  col: number,
) {
  for (let dy = -2; dy <= 2; dy += 1) {
    for (let dx = -2; dx <= 2; dx += 1) {
      const dark =
        dy === -2 || dy === 2 || dx === -2 || dx === 2 || (dx === 0 && dy === 0);
      setModule(grid, reserved, row + dy, col + dx, dark ? 1 : 0);
    }
  }
}

function finderOverlap(row: number, col: number, size: number) {
  return (
    (row <= 8 && col <= 8) ||
    (row <= 8 && col >= size - 9) ||
    (row >= size - 9 && col <= 8)
  );
}

function maskBit(mask: number, row: number, col: number) {
  switch (mask) {
    case 0:
      return (row + col) % 2 === 0;
    case 1:
      return row % 2 === 0;
    case 2:
      return col % 3 === 0;
    case 3:
      return (row + col) % 3 === 0;
    case 4:
      return (Math.floor(row / 2) + Math.floor(col / 3)) % 2 === 0;
    case 5:
      return ((row * col) % 2) + ((row * col) % 3) === 0;
    case 6:
      return (((row * col) % 2) + ((row * col) % 3)) % 2 === 0;
    default:
      return (((row + col) % 2) + ((row * col) % 3)) % 2 === 0;
  }
}

function placeFormat(
  grid: Int8Array[],
  reserved: Uint8Array[],
  bits: number,
  write: boolean,
) {
  const size = grid.length;
  for (let index = 0; index < 15; index += 1) {
    const dark = (bits >> index) & 1;
    const positions: Array<[number, number]> =
      index < 6
        ? [
            [8, index],
            [size - 1 - index, 8],
          ]
        : index === 6
          ? [
              [8, 7],
              [size - 7, 8],
            ]
          : index === 7
            ? [
                [8, 8],
                [8, size - 8],
              ]
            : index === 8
              ? [
                  [7, 8],
                  [8, size - 7],
                ]
              : [
                  [14 - index, 8],
                  [8, size - 15 + index],
                ];
    for (const [row, col] of positions) {
      if (write) grid[row][col] = dark;
      else reserved[row][col] = 1;
    }
  }
}

function placeVersion(grid: Int8Array[], reserved: Uint8Array[], bits: number) {
  const size = grid.length;
  for (let index = 0; index < 18; index += 1) {
    const dark = (bits >> index) & 1;
    const row = Math.floor(index / 3);
    const col = index % 3;
    setModule(grid, reserved, row, size - 11 + col, dark);
    setModule(grid, reserved, size - 11 + col, row, dark);
  }
}

function penalty(grid: Int8Array[]) {
  const size = grid.length;
  let score = 0;
  for (let row = 0; row < size; row += 1) {
    let run = 1;
    for (let col = 1; col <= size; col += 1) {
      if (col < size && grid[row][col] === grid[row][col - 1]) {
        run += 1;
        continue;
      }
      if (run >= 5) score += 3 + (run - 5);
      run = 1;
    }
  }
  for (let col = 0; col < size; col += 1) {
    let run = 1;
    for (let row = 1; row <= size; row += 1) {
      if (row < size && grid[row][col] === grid[row - 1][col]) {
        run += 1;
        continue;
      }
      if (run >= 5) score += 3 + (run - 5);
      run = 1;
    }
  }
  for (let row = 0; row < size - 1; row += 1) {
    for (let col = 0; col < size - 1; col += 1) {
      const value = grid[row][col];
      if (
        value === grid[row][col + 1] &&
        value === grid[row + 1][col] &&
        value === grid[row + 1][col + 1]
      ) {
        score += 3;
      }
    }
  }
  const finder = [1, 0, 1, 1, 1, 0, 1];
  const hasFinder = (line: Int8Array, start: number) =>
    finder.every((bit, index) => line[start + index] === bit);
  const countFinder = (line: Int8Array) => {
    let count = 0;
    for (let index = 0; index <= line.length - 11; index += 1) {
      if (
        hasFinder(line, index) &&
        line[index + 7] === 0 &&
        line[index + 8] === 0 &&
        line[index + 9] === 0 &&
        line[index + 10] === 0
      ) {
        count += 1;
      }
      if (
        line[index] === 0 &&
        line[index + 1] === 0 &&
        line[index + 2] === 0 &&
        line[index + 3] === 0 &&
        hasFinder(line, index + 4)
      ) {
        count += 1;
      }
    }
    return count;
  };
  for (let row = 0; row < size; row += 1) score += countFinder(grid[row]) * 40;
  for (let col = 0; col < size; col += 1) {
    const column = new Int8Array(size);
    for (let row = 0; row < size; row += 1) column[row] = grid[row][col];
    score += countFinder(column) * 40;
  }
  let dark = 0;
  for (let row = 0; row < size; row += 1) {
    for (let col = 0; col < size; col += 1) dark += grid[row][col];
  }
  const percent = (dark * 100) / (size * size);
  score += 10 * Math.floor(Math.abs(percent - 50) / 5);
  return score;
}

function buildMatrix(payload: Uint8Array, version: number, ecl: number) {
  const layout = BLOCKS[version - 1][ecl === 1 ? 0 : 1];
  const data = interleave(encodeData(payload, version, layout), layout);
  const size = 21 + 4 * (version - 1);
  const grid = Array.from({ length: size }, () => new Int8Array(size));
  const reserved = Array.from({ length: size }, () => new Uint8Array(size));

  placeFinder(grid, reserved, 0, 0);
  placeFinder(grid, reserved, 0, size - 7);
  placeFinder(grid, reserved, size - 7, 0);
  for (const row of ALIGNMENT[version - 1]) {
    for (const col of ALIGNMENT[version - 1]) {
      if (finderOverlap(row, col, size)) continue;
      placeAlignment(grid, reserved, row, col);
    }
  }
  for (let index = 8; index < size - 8; index += 1) {
    setModule(grid, reserved, 6, index, index % 2 === 0 ? 1 : 0);
    setModule(grid, reserved, index, 6, index % 2 === 0 ? 1 : 0);
  }
  setModule(grid, reserved, size - 8, 8, 1);
  placeFormat(grid, reserved, 0, false);
  if (version >= 7) placeVersion(grid, reserved, versionBits(version));

  const bits: number[] = [];
  for (const byte of data) pushBits(bits, byte, 8);
  for (let index = 0; index < remainderBits(version); index += 1) bits.push(0);

  let bit = 0;
  let upward = true;
  for (let col = size - 1; col > 0; col -= 2) {
    if (col === 6) col -= 1;
    for (let count = 0; count < size; count += 1) {
      const row = upward ? size - 1 - count : count;
      for (const offset of [0, 1]) {
        const x = col - offset;
        if (reserved[row][x]) continue;
        grid[row][x] = bits[bit] ?? 0;
        bit += 1;
      }
    }
    upward = !upward;
  }

  let bestScore = Infinity;
  let best: Int8Array[] | null = null;
  for (let mask = 0; mask < 8; mask += 1) {
    const candidate = grid.map((row) => row.slice());
    for (let row = 0; row < size; row += 1) {
      for (let col = 0; col < size; col += 1) {
        if (!reserved[row][col] && maskBit(mask, row, col)) {
          candidate[row][col] ^= 1;
        }
      }
    }
    placeFormat(candidate, reserved, formatBits(ecl, mask), true);
    const score = penalty(candidate);
    if (score < bestScore) {
      bestScore = score;
      best = candidate;
    }
  }
  return best ?? grid;
}

function matrixToSvg(grid: Int8Array[]) {
  const quiet = 4;
  const size = grid.length + quiet * 2;
  const parts = [`M0 0h${size}v${size}h-${size}z`];
  for (let row = 0; row < grid.length; row += 1) {
    for (let col = 0; col < grid.length; col += 1) {
      if (grid[row][col]) {
        parts.push(`M${col + quiet} ${row + quiet}h1v1h-1z`);
      }
    }
  }
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${size} ${size}" shape-rendering="crispEdges"><path fill="#fff" d="M0 0h${size}v${size}h-${size}z"/><path fill="#111" d="${parts.slice(1).join("")}"/></svg>`;
}

export function compactPairingJSON(text: string) {
  const value = JSON.parse(text) as unknown;
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("pairing bundle is not an object");
  }
  const record = value as Record<string, unknown>;
  const keys = Object.keys(record);
  if (
    keys.length !== PAIRING_KEYS.length ||
    PAIRING_KEYS.some((key) => !keys.includes(key))
  ) {
    throw new Error("pairing bundle has an unsupported shape");
  }
  return JSON.stringify({
    version: record.version,
    base_url: record.base_url,
    host_id: record.host_id,
    grant_id: record.grant_id,
    pairing_secret: record.pairing_secret,
    expires_at: record.expires_at,
  });
}

export function pairingQRSvg(prettyJSON: string) {
  const compact = compactPairingJSON(prettyJSON);
  const bytes = new TextEncoder().encode(compact);
  for (let version = 1; version <= 20; version += 1) {
    for (const ecl of [0, 1] as const) {
      const layout = BLOCKS[version - 1][ecl === 1 ? 0 : 1];
      const countBits = version <= 9 ? 8 : 16;
      const needed = 4 + countBits + bytes.length * 8;
      if (needed > dataCodewords(layout) * 8) continue;
      return matrixToSvg(buildMatrix(bytes, version, ecl));
    }
  }
  throw new Error("pairing bundle is too large for a QR code");
}
