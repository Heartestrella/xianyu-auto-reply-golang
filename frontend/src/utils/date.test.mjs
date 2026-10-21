import assert from 'node:assert/strict'
import test from 'node:test'
import { configureServerTime, formatDateTime, formatPlatformDateTime, getServerDateInputValue } from './date.ts'

test('UTC database timestamps use server time and cross midnight correctly', () => {
  configureServerTime(undefined, '480')
  assert.equal(formatDateTime('2026-10-19 16:04:34'), '2026/10/20 00:04:34')
  assert.equal(formatDateTime('2026-10-19T16:04:34Z'), '2026/10/20 00:04:34')
  assert.equal(formatDateTime('2026-10-20T00:04:34+08:00'), '2026/10/20 00:04:34')
  assert.equal(getServerDateInputValue(new Date('2026-10-19T16:04:34Z')), '2026-10-20')
  assert.equal(formatPlatformDateTime('2026-10-20 00:04:34'), '2026/10/20 00:04:34')
  assert.equal(formatPlatformDateTime(Date.parse('2026-10-19T16:04:34Z')), '2026/10/20 00:04:34')
  assert.equal(formatDateTime('invalid'), '-')
  assert.equal(formatDateTime(null), '-')
})

test('server offset and clock are independent from the browser timezone', () => {
  const now = Date.now()
  configureServerTime('2026-10-20T00:04:34+08:00', '-300')
  assert.equal(formatDateTime('2026-10-19 16:04:34'), '2026/10/19 11:04:34')
  assert.equal(getServerDateInputValue(), '2026-10-19')
  configureServerTime(new Date(now).toISOString(), '480')
})
