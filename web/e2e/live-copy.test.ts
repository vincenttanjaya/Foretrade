import { expect, it } from 'vitest'
import { createState, placeOrder, portfolioOf, createCopyPlan, advanceLiveStep, stopCopyPlan, liveBoardOf } from './mock-api'
it('copy starts empty, copies future deltas once, advances revision and stays owner specific',()=>{
  const s=createState(), other=createState(), before=portfolioOf(s), fills=s.trades.length
  const plan=createCopyPlan(s,'live-momentum',{ratio:0.1,maxUsd:100})
  expect(s.trades).toHaveLength(fills)
  expect(plan.positions).toEqual([])
  expect(portfolioOf(s)).toEqual(before)
  advanceLiveStep(s,27)
  expect(plan.positions[0].symbol).toBe('BTC')
  expect(plan.deployedUsd).toBeLessThanOrEqual(100)
  expect(s.trades[0].source).toBe('copy')
  expect(s.trades[0].ref?.id).toBe(plan.id)
  expect(portfolioOf(s).revision).toBeGreaterThan(before.revision!)
  advanceLiveStep(s,27)
  expect(s.trades).toHaveLength(fills+1)
  expect(liveBoardOf(other).myPlans).toEqual([])
})
it('close reconciles manual reductions across plans and does not sell another plan or open a short',()=>{
  const s=createState()
  const a=createCopyPlan(s,'live-momentum',{ratio:0.1,maxUsd:1000})
  const b=createCopyPlan(s,'live-macro',{ratio:0.1,maxUsd:1000})
  advanceLiveStep(s,27,'live-momentum');advanceLiveStep(s,27,'live-macro')
  const total=s.positions.get('BTC')!.qty
  placeOrder(s,{symbol:'BTC',side:'sell',qty:total/2})
  const out=stopCopyPlan(s,a.id,true)
  expect(out.trades[0].qty).toBeCloseTo(total/4)
  expect(s.positions.get('BTC')!.qty).toBeCloseTo(b.positions[0].qty)
  expect(s.positions.get('BTC')!.qty).toBeGreaterThan(0)
})
it('keep leaves account positions untouched and unavailable close keeps residuals with a log',()=>{
  const s=createState(), a=createCopyPlan(s,'live-momentum',{ratio:0.1,maxUsd:5000})
  advanceLiveStep(s,27)
  const qty=s.positions.get('BTC')!.qty
  expect(stopCopyPlan(s,a.id,false).trades).toEqual([])
  expect(s.positions.get('BTC')!.qty).toBe(qty)
  const b=createCopyPlan(s,'live-macro',{ratio:0.1,maxUsd:5000})
  advanceLiveStep(s,28,'live-macro')
  const out=stopCopyPlan(s,b.id,true,false)
  expect(out.plan.positions).toHaveLength(1)
  expect(out.plan.log.some((e) => /unavailable/.test(e.text))).toBe(true)
  expect(out.plan.log[0].text).toMatch(/1 residual holdings remain/)
  expect(out.plan.status).toBe('stopped')
})
