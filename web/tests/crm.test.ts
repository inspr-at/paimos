// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  NO_FILTER, addressLines, blankCustomer, customerWrite, facetOf, lineDiff, matchesCustomer, minorInput, minorMoney, normalizeWebsite, parseCount, parseMoney,
  placeOf, plainError, sortCustomers, tidyAddress, validEmail, validWebsite, visibleColumns, type Customer,
} from '../src/lib/crm.ts'

const customer = (name: string, extra: Partial<Customer> = {}): Customer => ({
  ...blankCustomer(name), id: name.toLowerCase(), key: `ORG-${name.length}`, revision: 1, customer_no: null, primary_contact_node_id: null, ...extra,
})
const at = (city: string, country: string) => ({ street: '', postal_code: '', city, country, freeform: '' })

test('a customer is written back whole, with nothing extra', () => {
  const c = customer('Bakery', { industry: 'Food', billing_address: at('Graz', 'AT'), customer_no: 'K-0042', hourly_rate_minor: 9500 })
  const write = customerWrite(c)
  assert.equal(write.name, 'Bakery')
  assert.equal(write.industry, 'Food')
  assert.equal(write.hourly_rate_minor, 9500)
  assert.deepEqual(write.billing_address, at('Graz', 'AT'))
  assert.equal(write.visiting_address, null)
  for (const extra of ['customer_no', 'revision', 'id', 'key', 'primary_contact_node_id']) assert.equal(extra in write, false)
})

test('addresses read as lines; an empty one is none', () => {
  assert.deepEqual(addressLines({ street: 'Herrengasse 1', postal_code: '8010', city: 'Graz', country: 'Austria', freeform: '' }), ['Herrengasse 1', '8010 Graz', 'Austria'])
  assert.deepEqual(addressLines({ ...at('', ''), freeform: 'PO Box 7\nVienna' }), ['PO Box 7', 'Vienna'])
  assert.equal(tidyAddress({ street: ' ', postal_code: '', city: '', country: '', freeform: '' }), null)
  assert.deepEqual(tidyAddress({ street: ' Main 1 ', postal_code: '', city: 'Linz', country: '', freeform: '' }), { street: 'Main 1', postal_code: '', city: 'Linz', country: '', freeform: '' })
  assert.equal(placeOf(customer('X', { visiting_address: at('Linz', 'AT') })), 'Linz, AT')
})

test('money in minor units reads and parses exactly', () => {
  assert.equal(minorMoney(9500, 'EUR'), '95.00 EUR')
  assert.equal(minorMoney(123456789, 'eur'), '1,234,567.89 EUR')
  assert.equal(minorMoney(9505, ''), '95.05')
  assert.equal(minorMoney(1200, 'JPY'), '1,200 JPY')
  assert.equal(minorMoney(null, 'EUR'), '')
  assert.equal(minorInput(9505, 'EUR'), '95.05')
  assert.equal(minorInput(null, 'EUR'), '')
  assert.equal(parseMoney('95', 'EUR'), 9500)
  assert.equal(parseMoney('1,250.5', 'EUR'), 125050)
  assert.equal(parseMoney('', 'EUR'), null)
  assert.equal(parseMoney('12.345', 'EUR'), 'invalid')
  assert.equal(parseMoney('-3', 'EUR'), 'invalid')
  assert.equal(parseCount('1.200'), 1200)
  assert.equal(parseCount('12a'), 'invalid')
})

test('web addresses and email addresses', () => {
  assert.equal(normalizeWebsite('hofer.at'), 'https://hofer.at')
  assert.equal(normalizeWebsite('http://hofer.at'), 'http://hofer.at')
  assert.equal(validWebsite('https://hofer.at'), true)
  assert.equal(validWebsite('ftp://hofer.at'), false)
  assert.equal(validEmail('jana@hofer.at'), true)
  assert.equal(validEmail('Jana <jana@hofer.at>'), false)
})

test('search, filters and sort; blanks sort last', () => {
  const list = [
    customer('Clinic', { customer_no: 'K-0002', billing_address: at('Vienna', 'AT'), industry: 'Health', hourly_rate_minor: 9000 }),
    customer('bakery', { customer_no: 'K-0010', billing_address: at('Graz', 'AT'), industry: 'Food' }),
    customer('Atelier', { billing_address: at('Munich', 'DE'), hourly_rate_minor: 12000 }),
  ]
  const pick = (f: Partial<typeof NO_FILTER>, contact?: string) => list.filter(c => matchesCustomer(c, { ...NO_FILTER, ...f }, contact ? { name: contact, email: '', role: '' } : undefined)).map(c => c.name)
  assert.deepEqual(pick({ q: 'k-0010' }), ['bakery'])
  assert.deepEqual(pick({ number: 'without' }), ['Atelier'])
  assert.deepEqual(pick({ countries: ['AT'] }), ['Clinic', 'bakery'])
  assert.deepEqual(pick({ industries: ['Food', 'Health'] }), ['Clinic', 'bakery'])
  assert.deepEqual(pick({ q: 'jana' }, 'Jana Novak'), ['Clinic', 'bakery', 'Atelier'])
  assert.deepEqual(sortCustomers(list, 'name', 'asc').map(c => c.name), ['Atelier', 'bakery', 'Clinic'])
  assert.deepEqual(sortCustomers(list, 'number', 'asc').map(c => c.name), ['Clinic', 'bakery', 'Atelier'])
  assert.deepEqual(sortCustomers(list, 'number', 'desc').map(c => c.name), ['bakery', 'Clinic', 'Atelier'])
  assert.deepEqual(sortCustomers(list, 'rate', 'desc').map(c => c.name), ['Atelier', 'Clinic', 'bakery'])
  assert.deepEqual(facetOf(list, c => c.billing_address?.country ?? ''), [{ value: 'AT', label: 'AT', count: 2 }, { value: 'DE', label: 'DE', count: 1 }])
})

test('columns step aside as the table narrows, Name and Number stay', () => {
  assert.deepEqual(visibleColumns(1400), ['name', 'number', 'contact', 'place', 'industry', 'rate'])
  assert.deepEqual(visibleColumns(900), ['name', 'number', 'contact', 'place'])
  assert.deepEqual(visibleColumns(420), ['name', 'number'])
  // A column the person widened makes others leave earlier.
  assert.deepEqual(visibleColumns(1100), ['name', 'number', 'contact', 'place', 'industry', 'rate'])
  assert.deepEqual(visibleColumns(1100, { contact: 420 }), ['name', 'number', 'contact', 'place', 'rate'])
})

test('spare width goes to the contact, place and industry, not to a wide Customer column', async () => {
  const { NAME_TARGET, layoutWidths } = await import('../src/lib/crm.ts')
  const all = ['name', 'number', 'contact', 'place', 'industry', 'rate'] as const
  const wide = layoutWidths([...all], 1382)
  const others = Object.values(wide).reduce((a, b) => a + (b ?? 0), 0)
  assert.ok(wide.contact! > 300, `contact ${wide.contact}`)
  assert.ok(1382 - others >= NAME_TARGET && 1382 - others <= NAME_TARGET + 3, `name ${1382 - others}`)
  // Narrow tables keep the defaults; a column the person sized keeps its width.
  assert.deepEqual(layoutWidths([...all], 1100), { number: 132, contact: 260, place: 190, industry: 160, rate: 136 })
  assert.equal(layoutWidths([...all], 1382, { contact: 200 }).contact, 200)
  // Maximums hold on very wide tables.
  assert.equal(layoutWidths([...all], 3000).contact, 460)
})

test('a note proposal reads as a line diff', () => {
  assert.deepEqual(lineDiff('a\nb\nc', 'a\nB\nc\nd'), [
    { kind: 'same', text: 'a' }, { kind: 'remove', text: 'b' }, { kind: 'add', text: 'B' }, { kind: 'same', text: 'c' }, { kind: 'add', text: 'd' },
  ])
  assert.deepEqual(lineDiff('', 'new'), [{ kind: 'add', text: 'new' }])
  assert.deepEqual(lineDiff('old', ''), [{ kind: 'remove', text: 'old' }])
})

test('errors say what happened', () => {
  assert.match(plainError(409, 'record changed'), /changed elsewhere/)
  assert.match(plainError(409, 'business_crm is not enabled for this operation'), /not enabled/)
  assert.match(plainError(400, 'invalid email'), /email address/)
  assert.match(plainError(403, ''), /admin/)
  assert.match(plainError(502, ''), /did not answer/)
})

test('edit mode: the draft round-trips and reports what it cannot save', async () => {
  const { draftOf, draftProblems, writeOf } = await import('../src/lib/crm.ts')
  const c = customer('Bakery', { currency: 'EUR', hourly_rate_minor: 9550, billing_address: at('Graz', 'AT'), external_provider: 'hubspot', external_id: '42', employee_count: 12 })
  const draft = draftOf(c)
  assert.equal(draft.hourly, '95.50')
  assert.equal(draft.employees, '12')
  assert.deepEqual(draftProblems(draft), {})
  const write = writeOf({ ...draft, website: 'hofer.at', hourly: '120', visiting: { ...draft.visiting } }, c)
  assert.equal(write.website, 'https://hofer.at')
  assert.equal(write.hourly_rate_minor, 12000)
  assert.equal(write.visiting_address, null)
  assert.deepEqual(write.billing_address, at('Graz', 'AT'))
  assert.equal(write.external_provider, 'hubspot')
  assert.deepEqual(Object.keys(draftProblems({ ...draft, name: ' ', currency: 'eu', hourly: '1.234', employees: 'many' })).sort(), ['currency', 'employees', 'hourly', 'name'])
  assert.equal(draftProblems({ ...draft, currency: '' }).currency, 'Amounts need a currency, such as EUR.')
})

test('newline-heavy accepted notes stay within the diff allocation and render budget', () => {
  const original = Array.from
  let cells = 0
  Array.from = ((input: ArrayLike<unknown>, ...args: unknown[]) => {
    if (input.length > 2_000) throw new Error('unbounded diff allocation')
    cells += input.length * input.length
    return Reflect.apply(original, Array, [input, ...args])
  }) as typeof Array.from
  try {
    const changed = lineDiff('a\n'.repeat(10_000), 'b\n'.repeat(10_000))
    assert.equal(changed.length, 1)
    assert.equal(changed[0].kind, 'summary')
    assert.match(changed[0].text, /Review the two full versions/)
    const equal = lineDiff('a\n'.repeat(10_000), 'a\n'.repeat(10_000))
    assert.match(equal[0].text, /identical/)
    assert.equal(cells, 0)
  } finally { Array.from = original }
})
