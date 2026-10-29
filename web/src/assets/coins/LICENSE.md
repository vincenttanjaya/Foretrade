# Coin logos

One round SVG per symbol of the trading universe, drawn by `AssetIcon`. They ship
with the app as hashed files; nothing is loaded from a third-party host.

## Sources

- **react-web3-icons** by derodero24, MIT, commit
  [`fccbb69`](https://github.com/derodero24/react-web3-icons/tree/fccbb69b46a6482c3f23b50c5b0a81c4598af225/icons)
  (2026-10-04). Each icon there is traced to the project's own brand kit or site; the
  `.json` next to it in `icons/` records the official source.
- **@web3icons/core** 4.0.57 by 0xa3k5, MIT
  ([raw-svgs](https://github.com/0xa3k5/web3icons/tree/main/raw-svgs)): PAXG and ETC,
  which react-web3-icons does not have.

Every file was optimized with SVGO 4 (two decimals, ids prefixed with the symbol).
Where upstream ships a bare mark, it is centred on a disc so all 40 read as coins:

| Symbol | Upstream file | Change |
| --- | --- | --- |
| BTC | `icons/chain/bitcoin.circle.svg` | - |
| ETH | `icons/chain/ethereum.circle.svg` | - |
| SOL | `icons/chain/solana.circle.svg` | - |
| BNB | `icons/coin/bnb.svg` | mark in white on a #F0B90B disc |
| XRP | `icons/coin/xrp.circle.svg` | - |
| DOGE | `icons/coin/doge.svg` | coordinates rounded to a 320-unit grid (54 kB to 15 kB) |
| ADA | `icons/chain/cardano.circle.svg` | - |
| AVAX | `icons/chain/avalanche.circle.svg` | - |
| LINK | `icons/devtool/chainlink.svg` | mark in white on a #0847F7 disc |
| SUI | `icons/chain/sui.svg` | mark in white on a #298DFF disc |
| TON | `icons/coin/gram.circle.svg` | - (Toncoin was renamed Gram on 2026-06-15; this is its badge) |
| PAXG | web3icons `tokens/background/PAXG.svg` | square tile made a disc |
| TRX | `icons/chain/tron.svg` | mark in white on a #EA0029 disc |
| DOT | `icons/chain/polkadot.svg` | mark in white on a #171717 disc |
| LTC | `icons/coin/ltc.svg` | - |
| BCH | `icons/coin/bch.svg` | - |
| NEAR | `icons/chain/near.svg` | mark on a #00EC97 disc |
| APT | `icons/chain/aptos.svg` | mark on a #D5FAD3 disc |
| ARB | `icons/chain/arbitrum.circle.svg` | - |
| OP | `icons/coin/op.svg` | - |
| UNI | `icons/dex/uniswap.svg` | mark on a white disc |
| AAVE | `icons/defi/aave.svg` | mark in white on a #9391F7 disc |
| ATOM | `icons/coin/atom.svg` | - |
| FIL | `icons/coin/fil.svg` | - |
| ICP | `icons/coin/icp.svg` | mark on a white disc |
| ETC | web3icons `tokens/background/ETC.svg` | square tile made a disc |
| XLM | `icons/chain/stellar.svg` | mark on a white disc |
| HBAR | `icons/coin/hbar.svg` | white behind the knocked-out H |
| INJ | `icons/coin/inj.svg` | - |
| SEI | `icons/chain/sei.svg` | white behind the knocked-out waves, scaled to the full disc |
| TIA | `icons/coin/tia.svg` | mark in white on Celestia Indigo #5640D1 |
| WLD | `icons/chain/world-chain.svg` | mark on a white disc |
| RENDER | `icons/coin/render.svg` | mark in white on a #DF0101 disc |
| FET | `icons/coin/fet.svg` (ASI Alliance symbol) | mark on a white disc |
| ENA | `icons/coin/ena.svg` | - |
| TAO | `icons/coin/tao.svg` | mark on a white disc |
| ONDO | `icons/coin/ondo.svg` | mark on a white disc |
| ALGO | `icons/chain/algorand.circle.svg` | - |
| LDO | `icons/coin/ldo.circle.svg` | - |
| STX | `icons/coin/stx.svg` | mark in white on a #FC6432 disc |

The logos are trademarks of their projects and are used only to identify each asset.
The licences below cover the drawings, not the marks.

## react-web3-icons

MIT License

Copyright (c) 2023 derodero24

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## @web3icons/core

MIT License

Copyright (c) 2024 0xa3k5

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Round 7 additional instrument artwork

29 locally bundled identifiers. Exact pinned sources, SHA256 hashes and adaptation notes are in [universe-provenance.json](universe-provenance.json). Genuine archive PNGs remain PNGs (MUBARAK, TRUMP, ACE, JTO, SAGA, and NIL; the wide Nillion docs wordmark was replaced with its correctly identified token PNG for badge legibility). The original 40 SVGs are unchanged.

MIT notices for react-web3-icons (derodero24, 2023), web3icons (0xa3k5, 2024), cryptocurrency-icons (Erik Thiart, 2018), Nillion (2024) and Smol (2024) are bundled in `notices/`.

JUP is Solana Jupiter, ZRO is LayerZero protocol. WLFI is the retrieved official World Liberty Financial SVG for representation under official brand guidance: its geometry is unchanged, with proportional clearspace and a white disc; no MIT, CC0 or CC BY grant is claimed. Marks identify instruments without implying issuer endorsement.

## Stocks, ETFs and indices

Every stock, ETF and index logo (US and Asia) lives in [`../stocks`](../stocks/LICENSE.md), with its convention, sources, licences and hashes. The stock files that Round 7 and the Asia wave bundled here (NVDA, TSLA, AAPL, MSFT, AMZN, GOOGL, META, SP500, XYZ100, BABA, HYUNDAI, SMSN), their provenance entries and the Font Awesome notice were removed when those were replaced.
