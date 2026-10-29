// Vitest, over HTTP: the dev server as `npm run dev` starts it (vite.config.ts). The 127.0.0.1 URL Vite prints must
// land on a page that can sign in (localhost), and ChatGPT sign-in's callback there must reach VITE_API_URL's API.
import { createServer as createHttpServer, request, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { join } from 'node:path'
import { createServer, type ViteDevServer } from 'vite'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'

let api: Server
let vite: ViteDevServer
let port = 0
const portOf = (s: Server | null | undefined) => (s!.address() as AddressInfo).port

beforeAll(async () => {
  api = createHttpServer((req, res) => res.end(`api saw ${req.url}`))
  await new Promise<void>((resolve) => api.listen(0, '127.0.0.1', resolve))
  process.env.VITE_API_URL = `http://127.0.0.1:${portOf(api)}/`
  vite = await createServer({
    configFile: join(import.meta.dirname, '../vite.config.ts'),
    server: { port: 0, watch: null },
    logLevel: 'silent',
  })
  await vite.listen()
  port = portOf(vite.httpServer as Server)
})
afterAll(async () => {
  delete process.env.VITE_API_URL
  await vite?.close()
  await new Promise((resolve) => api?.close(resolve))
})

/** GET through the dev server's listener as a browser on `host` sends it, without following redirects. */
const get = (host: string, path: string) =>
  new Promise<{ status: number; location?: string; body: string }>((resolve, reject) => {
    request({ host: '127.0.0.1', port, path, headers: { host: `${host}:${port}` } }, (res) => {
      let body = ''
      res.setEncoding('utf8').on('data', (c: string) => (body += c))
      res.on('end', () => resolve({ status: res.statusCode!, location: res.headers.location, body }))
    })
      .on('error', reject)
      .end()
  })

describe('dev server', () => {
  it('moves every page on 127.0.0.1 to localhost, the origin the API and its session cookie serve', async () => {
    expect(await get('127.0.0.1', '/settings?chatgpt=ok')).toMatchObject({ status: 302, location: `http://localhost:${port}/settings?chatgpt=ok` })
    const page = await get('localhost', '/settings')
    expect(page.status).toBe(200)
    expect(page.body).toContain('<div id="root">')
  })

  it("proxies 127.0.0.1's /auth/callback to the API in VITE_API_URL", async () => {
    expect(await get('127.0.0.1', '/auth/callback?code=c&state=s')).toMatchObject({ status: 200, body: 'api saw /auth/callback?code=c&state=s' })
  })
})
