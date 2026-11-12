import { crc32, deflateRawSync } from 'node:zlib';

/**
 * The advisory data of the renders (R-VULN-005, R-VULN-038): the UI development profile imports npm advisories from a mirror
 * repository instead of the network (deploy/ui-dev/up.sh), and the seed uploads an archive there whose records affect the
 * vendored packages below with chosen counts, so that every grade from S to E occurs. The records are the seed's own, named
 * DEMO-…; they describe no real vulnerability. The internal packages (claimed names) and the images, which the profile cannot
 * scan, are the grade N/A.
 */

/** The generic repository the profile's OSV source reads (`vulnerabilities.sources.osv.repository` in up.sh). */
export const advisoryMirror = { project: 'platform', key: 'advisory-mirror' } as const;

type Counts = Partial<Record<'critical' | 'high' | 'medium' | 'low' | 'unknown', number>>;

/**
 * Public packages vendored into a local repository that matches public advisories (`matchPublicAdvisories: always`), each with
 * the open findings its grade needs. The latest version of xml-stream-parser is fixed, so the list shows S and the package page
 * the C of 0.9.2; image-convert-tools carries the counts of the owner's reference card.
 */
export const vendorPackages: readonly { repo: string; name: string; version: string; counts: Counts; grade: string }[] = [
  { repo: 'npm-vendor', name: 'date-format-lite', version: '2.1.0', counts: {}, grade: 'S' },
  { repo: 'npm-vendor', name: 'tiny-colors', version: '1.2.0', counts: { low: 2 }, grade: 'A' },
  { repo: 'npm-vendor', name: 'yaml-loader-compat', version: '3.0.0', counts: { medium: 6, low: 4 }, grade: 'B' },
  { repo: 'npm-vendor', name: 'xml-stream-parser', version: '0.9.2', counts: { high: 3, medium: 5, unknown: 2 }, grade: 'C' },
  { repo: 'npm-vendor', name: 'xml-stream-parser', version: '1.0.0', counts: {}, grade: 'S' },
  { repo: 'npm-vendor', name: 'http-proxy-legacy', version: '1.18.0', counts: { critical: 3, high: 12, medium: 30, low: 8, unknown: 2 }, grade: 'D' },
  {
    repo: 'npm-vendor',
    name: 'image-convert-tools',
    version: '4.0.1',
    counts: { critical: 13, high: 160, medium: 113, low: 16, unknown: 843 },
    grade: 'E',
  },
];

/** The package whose page the renders open with the grade E. */
export const gradedPackage = { format: 'npm', name: 'image-convert-tools', version: '4.0.1' } as const;

const LEVEL: Record<keyof Counts, string | null> = { critical: 'CRITICAL', high: 'HIGH', medium: 'MODERATE', low: 'LOW', unknown: null };

/** One OSV record per finding a vendored version needs; the severity is the qualitative one of R-VULN-007 rule 4. */
function records(): { id: string; body: string }[] {
  const out: { id: string; body: string }[] = [];
  let n = 0;
  for (const p of vendorPackages) {
    for (const [level, count] of Object.entries(p.counts) as [keyof Counts, number][]) {
      for (let i = 0; i < count; i++) {
        const id = `DEMO-2026-${String(++n).padStart(5, '0')}`;
        const severity = LEVEL[level];
        out.push({
          id,
          body: JSON.stringify({
            schema_version: '1.6.0',
            id,
            modified: '2026-11-01T00:00:00Z',
            published: '2026-10-01T00:00:00Z',
            summary: `Demonstration advisory ${n} of the renders for ${p.name}`,
            details: 'Made up by the seed of make ui-renders, so that the screens show every grade. It describes no real vulnerability.',
            affected: [{ package: { ecosystem: 'npm', name: p.name }, versions: [p.version] }],
            ...(severity ? { database_specific: { severity } } : {}),
          }),
        });
      }
    }
  }
  return out;
}

/** A zip archive of deflated entries: the shape of the OSV dump `npm/all.zip`, one `<id>.json` per record. */
export function advisoryArchive(): Buffer {
  const locals: Buffer[] = [];
  const centrals: Buffer[] = [];
  let offset = 0;
  for (const r of records()) {
    const name = Buffer.from(`${r.id}.json`);
    const data = Buffer.from(r.body);
    const packed = deflateRawSync(data);
    const crc = crc32(data);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0, 6);
    local.writeUInt16LE(8, 8);
    local.writeUInt16LE(0, 10);
    local.writeUInt16LE(0x5b61, 12); // 2025-11-01
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(packed.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(name.length, 26);
    local.writeUInt16LE(0, 28);
    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(0, 8);
    central.writeUInt16LE(8, 10);
    central.writeUInt16LE(0, 12);
    central.writeUInt16LE(0x5b61, 14);
    central.writeUInt32LE(crc, 16);
    central.writeUInt32LE(packed.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt32LE(offset, 42);
    locals.push(local, name, packed);
    centrals.push(central, name);
    offset += local.length + name.length + packed.length;
  }
  const directory = Buffer.concat(centrals);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(centrals.length / 2, 8);
  end.writeUInt16LE(centrals.length / 2, 10);
  end.writeUInt32LE(directory.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...locals, directory, end]);
}
