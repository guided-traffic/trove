import * as tls from 'node:tls';

/** One TLS handshake against the system roots plus NODE_EXTRA_CA_CERTS; the name checked is `servername`. */
function verify(host: string, port: number, servername: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const socket = tls.connect({ host, port, servername, rejectUnauthorized: true }, () => {
      socket.end();
      resolve();
    });
    socket.setTimeout(10_000, () => socket.destroy(new Error('timeout')));
    socket.on('error', (err) => reject(new Error(`TLS to ${servername} via ${host}:${port}: ${err.message}`)));
  });
}

/**
 * Verifies the certificate chain of the edge and of the test IdP against the CA in NODE_EXTRA_CA_CERTS before
 * any journey runs. The browser contexts ignore HTTPS errors (Chromium reads no extra CA file), so this is the
 * place where a wrong or expired certificate stops the run.
 *
 * It then removes the users alice and bob: they are created just in time at their first sign-in (R-AUTH-007), and a
 * user left by an earlier run is bound to the subject of an identity provider that may since have been rebuilt,
 * which the sign-in refuses with `username_taken` (the same preparation E1.1 makes).
 */
export default async function globalSetup(): Promise<void> {
  if (!process.env['NODE_EXTRA_CA_CERTS']) {
    throw new Error('NODE_EXTRA_CA_CERTS is not set: the dev CA is needed to verify the endpoints');
  }
  for (const name of [
    'TROVE_E2E_ALICE_PASSWORD',
    'TROVE_E2E_BOB_PASSWORD',
    'TROVE_E2E_BREAKGLASS_PASSWORD',
    'TROVE_E2E_ADMIN_TOKEN',
  ]) {
    if (!process.env[name]) throw new Error(`${name} is not set`);
  }
  const edge = new URL(process.env['TROVE_E2E_EDGE_URL'] ?? 'https://localhost:8080');
  await verify(edge.hostname, Number(edge.port || 443), edge.hostname);
  const kc = process.env['TROVE_E2E_KEYCLOAK_ADDR'];
  if (kc) {
    const [host, port] = kc.split(':');
    await verify(host ?? '127.0.0.1', Number(port), 'trove-keycloak.trove.svc');
  }
  for (const user of ['alice', 'bob']) {
    const res = await fetch(`${edge.origin}/api/v1/users/${user}`, {
      method: 'DELETE',
      headers: { Authorization: `Bearer ${process.env['TROVE_E2E_ADMIN_TOKEN']}` },
    });
    if (res.status !== 204 && res.status !== 404) {
      throw new Error(`DELETE /api/v1/users/${user}: ${res.status} ${await res.text()}`);
    }
  }
}
