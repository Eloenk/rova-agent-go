const express = require('express');
const crypto = require('crypto');
const fs = require('fs');
const config = require('./config');
const { executeSwap, getSwapQuote } = require('./swapService');

const app = express();
app.disable('x-powered-by');
app.use(express.json({ limit: '16kb', strict: true }));

function requireSidecarToken(req, res, next) {
  const provided = req.get('authorization') || '';
  const expected = `Bearer ${config.sidecarToken}`;
  if (provided.length !== expected.length) {
    return res.status(401).json({ ok: false, error: 'Unauthorized' });
  }
  if (!crypto.timingSafeEqual(Buffer.from(provided), Buffer.from(expected))) {
    return res.status(401).json({ ok: false, error: 'Unauthorized' });
  }
  return next();
}

app.get('/health', (req, res) => {
  res.json({ ok: true, status: 'online', mode: config.tcpPort ? 'tcp' : 'uds' });
});

function validateSwapInput(body, walletRequired) {
  const { sellCurrency, buyCurrency, walletAddress } = body || {};
  const amount = Number(body && body.amount);
  if (!['USDC', 'EURC'].includes(sellCurrency) || !['USDC', 'EURC'].includes(buyCurrency)) {
    return { error: 'Only USDC and EURC are supported' };
  }
  if (sellCurrency === buyCurrency) {
    return { error: 'sellCurrency and buyCurrency must differ' };
  }
  if (!Number.isFinite(amount) || amount <= 0 || amount > config.maxSwapAmount) {
    return { error: `amount must be greater than zero and no more than ${config.maxSwapAmount}` };
  }
  if ((walletRequired || walletAddress) && !/^0x[a-fA-F0-9]{40}$/.test(walletAddress || '')) {
    return { error: 'A valid walletAddress is required' };
  }
  return { value: { sellCurrency, buyCurrency, amount, walletAddress } };
}

app.post('/api/swap/quote', requireSidecarToken, async (req, res) => {
  try {
    const input = validateSwapInput(req.body, true);
    if (input.error) return res.status(400).json({ ok: false, error: input.error });
    const quote = await getSwapQuote(input.value);
    res.json({ ok: true, quote });
  } catch (err) {
    console.error('[SwapSidecar] Quote request failed');
    res.status(502).json({ ok: false, error: 'Quote provider unavailable' });
  }
});

app.post('/api/swap', requireSidecarToken, async (req, res) => {
  try {
    const input = validateSwapInput(req.body, true);
    if (input.error) return res.status(400).json({ ok: false, error: input.error });
    if (!config.executionEnabled) {
      return res.status(403).json({ ok: false, error: 'Swap execution is disabled' });
    }
    const result = await executeSwap(input.value);

    res.json({ ok: true, txHash: result.txHash, quote: result.quote });
  } catch (err) {
    console.error('[SwapSidecar] Swap execution failed');
    res.status(502).json({ ok: false, error: 'Swap execution failed' });
  }
});

app.use((error, req, res, next) => {
  if (res.headersSent) return next(error);
  if (error && (error.type === 'entity.parse.failed' || error.type === 'entity.too.large')) {
    return res.status(400).json({ ok: false, error: 'Invalid JSON request body' });
  }
  console.error('[SwapSidecar] Request handling failed');
  return res.status(500).json({ ok: false, error: 'Internal server error' });
});

function hardenServer(server) {
  server.headersTimeout = 10_000;
  server.requestTimeout = 30_000;
  server.keepAliveTimeout = 5_000;
  server.on('error', () => {
    console.error('[SwapSidecar] Server failed');
    process.exitCode = 1;
  });
  return server;
}

if (config.tcpPort) {
  hardenServer(app.listen(config.tcpPort, '127.0.0.1', () => {
    console.log(`[SwapSidecar] HTTP server listening on 127.0.0.1:${config.tcpPort}`);
  }));
} else {
  if (fs.existsSync(config.socketPath)) {
    const existing = fs.lstatSync(config.socketPath);
    if (!existing.isSocket()) {
      throw new Error('Refusing to replace a non-socket file at ROVA_SWAP_SOCKET');
    }
    fs.unlinkSync(config.socketPath);
  }

  const server = hardenServer(app.listen(config.socketPath, () => {
    try {
      fs.chmodSync(config.socketPath, 0o600);
      console.log('[SwapSidecar] Unix domain socket server is ready');
    } catch (error) {
      console.error('[SwapSidecar] Failed to secure the Unix domain socket');
      server.close(() => {
        if (fs.existsSync(config.socketPath) && fs.lstatSync(config.socketPath).isSocket()) {
          fs.unlinkSync(config.socketPath);
        }
        process.exitCode = 1;
      });
    }
  }));
}
