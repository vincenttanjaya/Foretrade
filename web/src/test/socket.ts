/** Binance kline socket stand-in (stubbed for every test in setup.ts): never connects; tests drive its handlers. */
export class FakeWebSocket {
  static instances: FakeWebSocket[] = []
  onopen: (() => void) | null = null
  onmessage: ((e: { data: unknown }) => void) | null = null
  onclose: (() => void) | null = null
  closed = false
  constructor(readonly url: string) {
    FakeWebSocket.instances.push(this)
  }
  close() {
    this.closed = true
  }
}
