const express = require('express');
const fs = require('fs');
const path = require('path');
const { executeSwap, getSwapQuote } = require('./swapService');

const app = express();
app.use(express.json());

const SOCKET_PATH = process.env.ROVA_SWAP_SOCKET || '/tmp/rova-swap.sock';
const isWindows = process.platform === 'win32';
// On Linux/macOS, ignore generic PORT=8080 from environment and use UDS unless SWAP_SIDECAR_PORT is explicitly set
const explicitTcpPort = process.env.SWAP_SIDECAR_PORT || (isWindows ? (process.env.PORT || 3001) : null);

app.get('/health', (req, res) => {
  res.json({ ok: true, status: 'online', mode: explicitTcpPort ? 'tcp' : 'uds', socket: SOCKET_PATH });
});

app.post('/api/swap/quote', async (req, res) => {
  try {
    const { sellCurrency, buyCurrency, amount, walletAddress } = req.body;
    const quote = await getSwapQuote({ sellCurrency, buyCurrency, amount: Number(amount), walletAddress });
    res.json({ ok: true, quote });
  } catch (err) {
    console.error('[SwapSidecar] Quote error:', err.message);
    res.status(500).json({ ok: false, error: err.message });
  }
});

app.post('/api/swap', async (req, res) => {
  try {
    const { walletAddress, sellCurrency, buyCurrency, amount } = req.body;
    if (!walletAddress || !sellCurrency || !buyCurrency || !amount) {
      return res.status(400).json({ ok: false, error: 'Missing required parameters (walletAddress, sellCurrency, buyCurrency, amount)' });
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
  app.listen(explicitTcpPort, () => {
    console.log(`[SwapSidecar] HTTP Server listening on TCP port ${explicitTcpPort}`);
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
