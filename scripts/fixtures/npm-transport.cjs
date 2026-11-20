// TEST ONLY: redirect unchanged product and npm HTTPS calls to independent
// loopback fixtures. The shipped operation has no transport-routing option.
const https = require('https');
const http = require('http');
const original = https.request;
https.request = function(...args) {
  let options = {};
  let callback;
  if (typeof args[0] === 'string' || args[0] instanceof URL) {
    const url = new URL(args.shift());
    options = { protocol: url.protocol, hostname: url.hostname, port: url.port,
      path: url.pathname + url.search };
  }
  if (args[0] && typeof args[0] === 'object') Object.assign(options, args.shift());
  callback = args[0];
  const port = options.hostname === 'github.com' ? process.env.FIXTURE_ASSETS_PORT :
    options.hostname === 'registry.fixture.test' ? process.env.FIXTURE_REGISTRY_PORT : null;
  if (!port) throw new Error('Fixture forbids external network');
  return http.request({...options, protocol: 'http:', hostname: '127.0.0.1',
    host: '127.0.0.1', port, agent: false}, callback);
};
https.get = function(...args) { const request = https.request(...args); request.end(); return request; };
