import assert from 'node:assert/strict'
import test from 'node:test'
import {
  GOVERNANCE_ACTIONS,
  governanceActionsForStatus,
} from './governanceActions.ts'

// Row-menu governance visibility: which actions the menu offers per
// lifecycle status. Mirrors the backend transition map for the v1 action
// subset; the API still 400s on illegal transitions.

const idsFor = (status?: string | null) => governanceActionsForStatus(status).map((a) => a.id)

test('valid rows offer the full v1 action set in menu order', () => {
  assert.deepEqual(idsFor('valid'), [
    'submit-review',
    'confirm-review',
    'mark-expired',
    'delist',
    'archive',
  ])
})

test('missing lifecycle_status is treated as valid (legacy rows)', () => {
  assert.deepEqual(idsFor(undefined), idsFor('valid'))
  assert.deepEqual(idsFor(''), idsFor('valid'))
  assert.deepEqual(idsFor(null), idsFor('valid'))
})

test('draft rows can only be submitted for review', () => {
  assert.deepEqual(idsFor('draft'), ['submit-review'])
})

test('review-due rows offer the full v1 set (archive included)', () => {
  assert.deepEqual(idsFor('pending_review_cycle'), [
    'submit-review',
    'confirm-review',
    'mark-expired',
    'delist',
    'archive',
  ])
})

test('stale rows match review-due rows', () => {
  assert.deepEqual(idsFor('stale'), idsFor('pending_review_cycle'))
})

test('conflict rows keep submit/confirm/delist/archive', () => {
  assert.deepEqual(idsFor('conflict'), [
    'submit-review',
    'confirm-review',
    'delist',
    'archive',
  ])
})

test('terminal states only allow archiving (expired/replaced)', () => {
  assert.deepEqual(idsFor('expired'), ['archive'])
  assert.deepEqual(idsFor('replaced'), ['archive'])
})

test('quiet states offer nothing', () => {
  assert.deepEqual(idsFor('pending_review'), [])
  assert.deepEqual(idsFor('pending_effective'), [])
  assert.deepEqual(idsFor('delisted'), [])
  assert.deepEqual(idsFor('archived'), [])
})

test('matching is case-insensitive', () => {
  assert.deepEqual(idsFor('VALID'), idsFor('valid'))
  assert.deepEqual(idsFor(' Stale '), idsFor('stale'))
})

test('action metadata carries the transition payload and tone', () => {
  const byId = new Map(GOVERNANCE_ACTIONS.map((a) => [a.id, a]))
  // submit-review posts to /review/submit, not the transition endpoint.
  assert.equal(byId.get('submit-review')?.transitionAction, undefined)
  assert.equal(byId.get('confirm-review')?.transitionAction, 'confirm_review')
  assert.equal(byId.get('mark-expired')?.transitionAction, 'mark_expired')
  assert.equal(byId.get('delist')?.transitionAction, 'delist')
  assert.equal(byId.get('archive')?.transitionAction, 'archive')
  assert.equal(byId.get('archive')?.danger, true)
  for (const action of GOVERNANCE_ACTIONS) {
    assert.match(action.labelKey, /^knowledgeGovernance\./, action.id)
    assert.ok(action.icon, action.id)
  }
})
