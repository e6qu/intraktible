// SPDX-License-Identifier: AGPL-3.0-or-later
// CloudFront Function (viewer-request), attached ONLY to the S3 static behavior. It
// rewrites extensionless paths (client-side SPA routes like /cases/123) to /index.html
// so the adapter-static shell handles routing, while real assets (/_app/foo.js, .wasm,
// .css) — which carry a file extension — pass through untouched. It is NOT attached to
// the /v1 behaviors, so genuine API 403/404s are never masked as the SPA shell.
function handler(event) {
    var req = event.request;
    var uri = req.uri;
    var lastSegment = uri.substring(uri.lastIndexOf('/') + 1);
    if (lastSegment.indexOf('.') === -1) {
        req.uri = '/index.html';
    }
    return req;
}
