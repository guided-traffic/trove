import * as tls from 'node:tls';

/**
 * The global setup of the upgrade journey: one TLS handshake against the edge, verified against the dev CA in
 * NODE_EXTRA_CA_CERTS. The base setup also verifies the test IdP and deletes the users alice and bob, which the upgrade
 * journey neither uses nor may touch while a release is being replaced under it.
 */
export default async function globalSetup(): Promise<void> {
  if (!process.env['NODE_EXTRA_CA_CERTS']) {
    throw new Error('NODE_EXTRA_CA_CERTS is not set: the dev CA is needed to verify the edge');
  }
  const edge = new URL(process.env['TROVE_E2E_EDGE_URL'] ?? 'https://localhost:8080');
  await new Promise<void>((resolve, reject) => {
    const socket = tls.connect({ host: edge.hostname, port: Number(edge.port || 443), servername: edge.hostname, rejectUnauthorized: true }, () => {
      socket.end();
      resolve();
    });
    socket.setTimeout(10_000, () => socket.destroy(new Error('timeout')));
    socket.on('error', (err) => reject(new Error(`TLS to ${edge.hostname}:${edge.port}: ${err.message}`)));
  });
}
