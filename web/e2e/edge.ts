// The edge's stand-ins' certificate, shared by the flow tests' harnesses (primary.ts, home.ts): one for the household's
// names (*.<domain>), self-signed, which Chromium is told to trust by its public key's hash, and the flags that lead
// those names to the stand-in on this machine.
import { execFile } from 'node:child_process';
import { createHash, X509Certificate } from 'node:crypto';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { promisify } from 'node:util';

const run = promisify(execFile);

/** A certificate and its key for *.<domain>, and the hash of its public key. */
export type EdgeTLS = { key: Buffer; cert: Buffer; spki: string };

export async function edgeCertificate(domain: string): Promise<EdgeTLS> {
  const dir = await mkdtemp(path.join(tmpdir(), 'homeward-edge-'));
  try {
    const [key, cert] = [path.join(dir, 'tls.key'), path.join(dir, 'tls.crt')];
    await run('openssl', ['req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-keyout', key, '-out', cert,
      '-days', '1', '-subj', `/CN=*.${domain}`, '-addext', `subjectAltName=DNS:*.${domain}`]);
    const [k, c] = await Promise.all([readFile(key), readFile(cert)]);
    const spki = createHash('sha256').update(new X509Certificate(c).publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
    return { key: k, cert: c, spki };
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

/** Chromium's flags: the household's names lead to 127.0.0.1, where the edge's stand-in's certificate is trusted. */
export function edgeArgs(domain: string, tls: EdgeTLS): string[] {
  return [`--host-resolver-rules=MAP *.${domain} 127.0.0.1`, `--ignore-certificate-errors-spki-list=${tls.spki}`];
}
