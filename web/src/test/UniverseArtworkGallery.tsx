import { createRoot } from 'react-dom/client'
import { AssetIcon } from '../components/ui/AssetIcon'
import { SYMBOLS } from '../pages/strategies/strategy'
import listedLogos from '../assets/stocks/listed-logos.json'
import '../index.css'

const params = new URLSearchParams(location.search)
const theme = params.get('theme') === 'dark' ? 'dark' : 'light'
const listed = Object.entries(listedLogos).filter(([, logo]) => 'file' in logo).map(([symbol, logo]) => [symbol, 'name' in logo && typeof logo.name === 'string' ? logo.name : symbol])
const symbols = params.get('symbols')?.split(',')
const rows = symbols ? [...SYMBOLS, ...listed].filter(([symbol]) => symbols.includes(symbol)) : params.has('listed') ? listed : SYMBOLS.slice(40)
document.documentElement.dataset.theme = theme
createRoot(document.getElementById('root')!).render(
  <main className="mx-auto max-w-3xl bg-bg px-3 py-4 text-text">
    <h1 className="mb-4 text-lg font-medium">Universe artwork · {theme}</h1>
    <table className="w-full table-fixed text-left text-xs">
      <thead><tr><th className="w-[40%]">Instrument</th>{[20, 28, 40].map((size) => <th key={size}>{size} px</th>)}</tr></thead>
      <tbody>{rows.map(([symbol, name]) => (
        <tr key={symbol} data-symbol={symbol} className="h-16 border-b border-border">
          <th className="pr-2 font-normal"><span className="block font-mono font-medium">{symbol}</span><span className="block break-words text-muted">{name}</span></th>
          {[20, 28, 40].map((size) => <td key={size}><AssetIcon symbol={symbol} name={name} size={size} /></td>)}
        </tr>
      ))}</tbody>
    </table>
  </main>,
)
