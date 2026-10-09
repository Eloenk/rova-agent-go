const express = require('express');
const crypto = require('crypto');
const fs = require('fs');
const { executeSwap, getSwapQuote } = require('./swapService');

const app = express();
app.use(express.json());

const SOCKET_PATH = process.env.ROVA_SWAP_SOCKET || '/tmp/rova-swap.sock';
const explicitTcpPort = process.env.SWAP_SIDECAR_PORT || null;
const sidecarToken = process.env.ROVA_SIDECAR_TOKEN || '';

function requireSidecarToken(req, res, next) {
  const provided = req.get('authorization') || '';
  const expected = `Bearer ${sidecarToken}`;
  if (sidecarToken.length < 32 || provided.length !== expected.length) {
    return res.status(401).json({ ok: false, error: 'Unauthorized' });
  }
  if (!crypto.timingSafeEqual(Buffer.from(provided), Buffer.from(expected))) {
    return res.status(401).json({ ok: false, error: 'Unauthorized' });
  }
  return next();
}

app.get('/health', (req, res) => {
  res.json({ ok: true, status: 'online', mode: explicitTcpPort ? 'tcp' : 'uds', socket: SOCKET_PATH });
});

app.post('/api/swap/quote', requireSidecarToken, async (req, res) => {
  try {
    const { sellCurrency, buyCurrency, amount, walletAddress } = req.body;
    const quote = await getSwapQuote({ sellCurrency, buyCurrency, amount: Number(amount), walletAddress });
    res.json({ ok: true, quote });
  } catch (err) {
    console.error('[SwapSidecar] Quote error:', err.message);
    res.status(500).json({ ok: false, error: err.message });
  }
});

app.post('/api/swap', requireSidecarToken, async (req, res) => {
  try {
    const { walletAddress, sellCurrency, buyCurrency, amount } = req.body;
    if (!walletAddress || !sellCurrency || !buyCurrency || !amount) {
      return res.status(400).json({ ok: false, error: 'Missing required parameters (walletAddress, sellCurrency, buyCurrency, amount)' });
    }
    if (!/^0x[a-fA-F0-9]{40}$/.test(walletAddress) || !['USDC', 'EURC'].includes(sellCurrency) || !['USDC', 'EURC'].includes(buyCurrency)) {
      return res.status(400).json({ ok: false, error: 'Invalid wallet or currency' });
    }

    const result = await executeSwap({
      walletAddress,
      sellCurrency,
      buyCurrency,
      amount: Number(amount),
    });

    res.json({ ok: true, txHash: result.txHash, quote: result.quote });
  } catch (err) {
    console.error('[SwapSidecar] Execution error:', err.message);
    res.status(500).json({ ok: false, error: err.message });
  }
});

if (explicitTcpPort) {
  app.listen(explicitTcpPort, '127.0.0.1', () => {
    console.log(`[SwapSidecar] HTTP Server listening on 127.0.0.1:${explicitTcpPort}`);
  });
} else {
  if (fs.existsSync(SOCKET_PATH)) {
    try {
      fs.unlinkSync(SOCKET_PATH);
    } catch (e) {
      console.warn(`[SwapSidecar] Could not remove existing socket file ${SOCKET_PATH}:`, e.message);
    }
  }

  app.listen(SOCKET_PATH, () => {
    try {
      fs.chmodSync(SOCKET_PATH, '0600');
    } catch (e) {}
    console.log(`[SwapSidecar] Unix Domain Socket server listening on UDS: ${SOCKET_PATH}`);
  });
}
