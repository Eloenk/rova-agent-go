const { SwapKit, SwapChain } = require("@circle-fin/swap-kit");
const { createCircleWalletsAdapter } = require("@circle-fin/adapter-circle-wallets");
const config = require('./config');

let cachedKit = null;
let cachedAdapter = null;

function getKit() {
  if (!cachedKit) {
    cachedKit = new SwapKit({ disableAnalytics: true, disableErrorReporting: true });
  }
  return cachedKit;
}

function getAdapter() {
  if (cachedAdapter) return cachedAdapter;
  if (!config.circleAPIKey || !/^(?:0x)?[a-fA-F0-9]{64}$/.test(config.circleEntitySecret)) {
    throw new Error("Circle API key and 32-byte entity secret are required");
  }
  cachedAdapter = createCircleWalletsAdapter({
    apiKey: config.circleAPIKey,
    entitySecret: config.circleEntitySecret.replace(/^0x/, ''),
  });
  return cachedAdapter;
}

function getSwapChain() {
  const resolved = SwapChain[config.arcChain];
  if (!resolved) {
    throw new Error("ARC_CHAIN must be a supported SwapChain identifier");
  }
  return resolved;
}

async function getSwapQuote({ sellCurrency, buyCurrency, amount, walletAddress }) {
  const slippageBps = 50;
  const address = walletAddress || "0x0000000000000000000000000000000000000000";

  const result = await getKit().estimate({
    from: { adapter: getAdapter(), chain: getSwapChain(), address },
    tokenIn: sellCurrency,
    tokenOut: buyCurrency,
    amountIn: String(amount),
    config: { apiKey: config.circleAPIKey, slippageBps },
  });

  const estimatedBuyAmount = Number(result.estimatedOutput.amount);
  const exchangeRate = amount > 0 ? estimatedBuyAmount / amount : 0;
  const minBuyAmount = estimatedBuyAmount * (1 - slippageBps / 10000);

  return {
    sellCurrency,
    buyCurrency,
    sellAmount: amount,
    estimatedBuyAmount: Number(estimatedBuyAmount.toFixed(6)),
    exchangeRate: Number(exchangeRate.toFixed(6)),
    slippageBps,
    minBuyAmount: Number(minBuyAmount.toFixed(6)),
  };
}

async function executeSwap({ walletAddress, sellCurrency, buyCurrency, amount, maxSlippageBps = 50 }) {
	if (!config.executionEnabled) {
		throw new Error('Swap execution is disabled until ROVA_EXECUTION_ENABLED=true is explicitly approved');
	}
  if (sellCurrency === buyCurrency) {
    throw new Error("sellCurrency and buyCurrency must differ");
  }
  if (!amount || amount <= 0) {
    throw new Error("amount must be positive");
  }
  if (!walletAddress) {
    throw new Error("walletAddress is required for swap execution");
  }
  if (!['USDC', 'EURC'].includes(sellCurrency) || !['USDC', 'EURC'].includes(buyCurrency)) {
    throw new Error('Only USDC and EURC swaps are supported');
  }
	if (!Number.isFinite(amount) || amount > config.maxSwapAmount) {
		throw new Error(`amount must be no more than ${config.maxSwapAmount}`);
	}

  const quote = await getSwapQuote({ sellCurrency, buyCurrency, amount, walletAddress });
  const slippageBps = maxSlippageBps || 50;

  console.log('[SwapSidecar] Executing approved swap');

  const params = {
    from: { adapter: getAdapter(), chain: getSwapChain(), address: walletAddress },
    tokenIn: sellCurrency,
    tokenOut: buyCurrency,
    amountIn: String(amount),
  };

  const baseConfig = {
    apiKey: config.circleAPIKey,
    slippageBps,
  };

  let result;
  try {
    result = await getKit().swap({ ...params, config: baseConfig });
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    if (/undeployed wallet/i.test(msg)) {
      result = await getKit().swap({
        ...params,
        config: { ...baseConfig, allowanceStrategy: "approve" },
      });
    } else {
      console.error("[SwapSidecar] Swap provider request failed");
      throw err;
    }
  }

  return {
    txHash: result.txHash,
    success: true,
    quote,
  };
}

module.exports = {
  getSwapQuote,
  executeSwap,
};
