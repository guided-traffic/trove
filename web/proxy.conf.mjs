// Proxy of the dev server's `local` configuration (npm run start:local, make ui-dev-web): every path outside
// /ui/ goes to the trove of the UI development profile (deploy/ui-dev). That trove has
// https://localhost:4200 as its base URL, so the session cookies, the origin check of R-AUTH-012 and its
// redirects behave as behind a real edge. The Host header stays the dev server's; X-Forwarded-For carries
// the browser's address (trove trusts 127.0.0.1 as its edge). The backend's certificate is verified against
// the profile's CA, which the npm script hands to Node.js through NODE_EXTRA_CA_CERTS.
// docs/developer/web-ui.md#local-ui-development
const target = process.env['TROVE_UI_DEV_BACKEND'] ?? 'https://localhost:19080';

export default {
  '^/(?!ui(/|$))': { target, secure: true, changeOrigin: false, xfwd: true },
  // A page of the app whose path ends in a file name (the file view, /ui/projects/<p>/browse/<repo>/README.md) gets the app:
  // the dev server serves its index only for paths without an extension it knows. Every other request under /ui/ goes on as is.
  '^/ui/.*\\.[^/]+$': {
    target,
    bypass: (req) => (req.headers['sec-fetch-dest'] === 'document' ? '/ui/index.html' : req.url),
  },
};
