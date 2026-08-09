require("dotenv").config({ path: "../.env.local" });
require("dotenv").config({ path: "../.env" });
require("dotenv").config();

const { AppKit, SwapChain } = require("@circle-fin/app-kit");
const { createCircleWalletsAdapter } = require("@circle-fin/adapter-circle-wallets");

let cachedKit = null;
let cachedAdapter = null;

function getKit() {
  if (!cachedKit) cachedKit = new AppKit();
  return cachedKit;
}

function getAdapter() {
  if (cachedAdapter) return cachedAdapter;
  const apiKey = process.env.CIRCLE_API_KEY;
  const entitySecret = process.env.CIRCLE_ENTITY_SECRET;
  if (!apiKey || !entitySecret) {
    throw new Error("CIRCLE_API_KEY / CIRCLE_ENTITY_SECRET not configured in environment");
  }
  cachedAdapter = createCircleWalletsAdapter({ apiKey, entitySecret });
  return cachedAdapter;
}

function getSwapChain() {
  const chainKey = process.env.NEXT_PUBLIC_ARC_CHAIN || "Arc_Testnet";
  const resolved = SwapChain[chainKey];
  if (!resolved) {
    throw new Error(`NEXT_PUBLIC_ARC_CHAIN must be a SwapChain identifier (got "${chainKey}")`);
  }
  return resolved;
}

function getKitKey() {
  const kitKey = process.env.KIT_KEY || process.env.NEXT_PUBLIC_CIRCLE_KIT_KEY || "";
  if (!kitKey) {
    throw new Error("KIT_KEY not configured — required for AppKit swap");
  }
  return kitKey;
}

async function getSwapQuote({ sellCurrency, buyCurrency, amount, walletAddress }) {
  const slippageBps = 50;
  const address = walletAddress || "0x0000000000000000000000000000000000000000";

  const result = await getKit().estimateSwap({
    from: { adapter: getAdapter(), chain: getSwapChain(), address },
    tokenIn: sellCurrency,
    tokenOut: buyCurrency,
    amountIn: String(amount),
    config: { kitKey: getKitKey() },
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
  if (sellCurrency === buyCurrency) {
    throw new Error("sellCurrency and buyCurrency must differ");
  }
  if (!amount || amount <= 0) {
    throw new Error("amount must be positive");
  }
  if (!walletAddress) {
    throw new Error("walletAddress is required for swap execution");
  }

  const quote = await getSwapQuote({ sellCurrency, buyCurrency, amount, walletAddress });
  const slippageBps = maxSlippageBps || 50;

  console.log(`[SwapSidecar] Executing swap: ${amount} ${sellCurrency} -> ~${quote.estimatedBuyAmount} ${buyCurrency} for ${walletAddress}`);

  const params = {
    from: { adapter: getAdapter(), chain: getSwapChain(), address: walletAddress },
    tokenIn: sellCurrency,
    tokenOut: buyCurrency,
    amountIn: String(amount),
  };

  const baseConfig = {
    kitKey: getKitKey(),
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
      console.error("[SwapSidecar] kit.swap() failed:", msg);
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
