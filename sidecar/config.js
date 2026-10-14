const path = require('path');

function readEnv(name, fallback = '') {
  const value = process.env[name];
  return typeof value === 'string' && value.trim() !== '' ? value.trim() : fallback;
}

function readPositiveNumber(name, fallback) {
  const raw = readEnv(name, String(fallback));
  const value = Number(raw);
  if (!Number.isFinite(value) || value <= 0) {
    throw new Error(`${name} must be a positive number`);
  }
  return value;
}

function readOptionalPort(name) {
  const raw = readEnv(name);
  if (!raw) return null;
  const value = Number(raw);
  if (!Number.isInteger(value) || value < 1 || value > 65535) {
    throw new Error(`${name} must be an integer from 1 to 65535`);
  }
  return value;
}

const sidecarToken = readEnv('ROVA_SIDECAR_TOKEN');
if (sidecarToken.length < 32) {
  throw new Error('ROVA_SIDECAR_TOKEN must be configured with at least 32 characters');
}

const socketPath = readEnv('ROVA_SWAP_SOCKET', '/tmp/rova-swap.sock');
if (socketPath.includes('\0') || !path.isAbsolute(socketPath)) {
  throw new Error('ROVA_SWAP_SOCKET must be an absolute path');
}

const config = Object.freeze({
  arcChain: readEnv('ARC_CHAIN', readEnv('NEXT_PUBLIC_ARC_CHAIN', 'Arc_Testnet')),
  circleAPIKey: readEnv('CIRCLE_API_KEY'),
  circleEntitySecret: readEnv('CIRCLE_ENTITY_SECRET'),
  executionEnabled: readEnv('ROVA_EXECUTION_ENABLED', 'false').toLowerCase() === 'true',
  maxSwapAmount: readPositiveNumber('ROVA_MAX_AUTONOMOUS_AMOUNT_USDC', 100),
  sidecarToken,
  socketPath,
  tcpPort: readOptionalPort('SWAP_SIDECAR_PORT'),
});

module.exports = config;
