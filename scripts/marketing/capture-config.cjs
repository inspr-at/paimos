// Shared, side-effect-free configuration for the synthetic marketing stack.
const path = require('node:path');
const ROOT = path.resolve(__dirname, '../..');

function loopbackOrigin(value, name) {
  let url;
  try { url = new URL(value); } catch { throw new Error(`${name} must be a loopback HTTP origin`); }
  if (url.protocol !== 'http:' || !['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
    || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new Error(`${name} must be a loopback HTTP origin without credentials, path, query or fragment`);
  }
  return url.origin;
}

function absolutePath(value, name) {
  if (!path.isAbsolute(value) || /[\0\r\n]/.test(value)) throw new Error(`${name} must be an absolute path`);
  return path.resolve(value);
}

function captureConfig(environment = process.env, siteArgument) {
  const apiUrl = loopbackOrigin(environment.PAIMOS_CAPTURE_API_URL || 'http://localhost:8888', 'PAIMOS_CAPTURE_API_URL');
  const appUrl = loopbackOrigin(environment.PAIMOS_CAPTURE_APP_URL || 'http://localhost:5173', 'PAIMOS_CAPTURE_APP_URL');
  if (new URL(apiUrl).hostname !== new URL(appUrl).hostname) {
    throw new Error('capture API and app must use the same loopback host so login cookies stay scoped correctly');
  }
  const dataDir = absolutePath(environment.PAIMOS_CAPTURE_DATA_DIR || path.join(ROOT, 'data'), 'PAIMOS_CAPTURE_DATA_DIR');
  const site = siteArgument || environment.PAIMOS_CAPTURE_SITE_DIR || path.resolve(ROOT, '../inspr-at');
  if (/[\0\r\n]/.test(site)) throw new Error('capture site path contains a control character');
  return { apiUrl, appUrl, dataDir, database: path.join(dataDir, 'paimos.db'), siteRoot: path.resolve(site) };
}

module.exports = { captureConfig };
if (require.main === module) {
  try {
    const [field, site] = process.argv.slice(2);
    const config = captureConfig(process.env, site);
    if (field === '--json') console.log(JSON.stringify(config));
    else if (Object.hasOwn(config, field)) console.log(config[field]);
    else throw new Error('usage: capture-config.cjs --json|apiUrl|appUrl|dataDir|database|siteRoot [site]');
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
