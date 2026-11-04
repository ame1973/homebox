import type { components, operations } from '../src/lib/api/schema'

type Record = components['schemas']['OfflineRecord']

const stock = {
  entity_type: 'stock', entity_id: '["item-1","location-1"]', entity_revision: 1,
  operation: 'upsert', projection_version: 1,
  payload: {
    item_id: 'item-1', location_id: 'location-1', quantity_base: 250,
    base_unit: 'g', unit_code: 'kg', scale_factor: 1000,
  },
} as const satisfies Record

const tombstone = {
  entity_type: 'barcode', entity_id: 'barcode-1', entity_revision: 2,
  operation: 'delete', projection_version: 1, payload: null,
} as const satisfies Record

// @ts-expect-error A tombstone must never carry a payload.
const invalidDelete: Record = { ...tombstone, payload: stock.payload }
// @ts-expect-error Frozen records require an entity revision.
const missingRevision: Record = { entity_type: 'item', entity_id: 'item-1', operation: 'delete', projection_version: 1, payload: null }
// @ts-expect-error Stock quantities cannot be strings.
const invalidQuantity: Record = { ...stock, payload: { ...stock.payload, quantity_base: '250' } }

type AuthHeaders = operations['post_auth_password']['parameters']['header']
const authHeaders = {
  Origin: 'https://homebox.example.internal', 'X-CSRF-Token': 'csrf-fixture',
  'Idempotency-Key': 'explicit-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev',
} as const satisfies AuthHeaders

// @ts-expect-error Protected authentication writes require Origin as well as CSRF.
const noOrigin: AuthHeaders = { 'X-CSRF-Token': 'csrf-fixture', 'Idempotency-Key': 'explicit-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error Origin alone is not a session-bound request proof.
const noCSRF: AuthHeaders = { Origin: 'https://homebox.example.internal', 'Idempotency-Key': 'explicit-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error Opaque session credentials must never become Admin JSON fields.
const rawCredentialAdmin: components['schemas']['Admin'] = { id: 'admin-1', revision: 1, username: 'owner', must_change_password: false, session_token: 'must-not-be-exposed' }
// @ts-expect-error Login accepts the password, not a caller-supplied database hash.
const hashLogin: components['schemas']['LoginInput'] = { username: 'owner', password_hash: 'must-not-be-accepted' }

type LocationCreateHeaders = operations['post_locations']['parameters']['header']
const childLocationHeaders = { ...authHeaders, 'If-Match': '"3"' } satisfies LocationCreateHeaders
const rootLocationHeaders = { ...authHeaders } satisfies LocationCreateHeaders
// @ts-expect-error Domain writes require the configured browser Origin as well.
const locationWithoutOrigin: LocationCreateHeaders = { 'X-CSRF-Token': 'csrf-fixture', 'Idempotency-Key': 'location-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
const rootLocation = { name: 'Box', code: 'BOX-1', parent_id: null, sort_order: -1 } satisfies components['schemas']['LocationCreate']
const minimalLocation = { name: 'Box', code: 'BOX-1' } satisfies components['schemas']['LocationCreate']
const minimalItem = { name: 'Item' } satisfies components['schemas']['ItemCreate']
const rootMove = { target_parent_id: null } satisfies components['schemas']['LocationMove']
// @ts-expect-error The internal presence wrapper is not the nullable wire field.
const parentWrapper: components['schemas']['LocationWrite'] = { parent_id: { set: true, value: null } }
// @ts-expect-error Sort order is a safe integer on the wire, never text.
const textSortOrder: components['schemas']['LocationCreate'] = { name: 'Box', code: 'BOX-1', sort_order: '1' }
// @ts-expect-error Only the parent field is nullable; kind is not.
const nullKind: components['schemas']['LocationWrite'] = { kind: null }

declare const createLocation: typeof import('../src/lib/api/locations').createLocation
declare const writeContext: import('../src/lib/api/client').WriteContext
const createRoot = createLocation({ name: 'Box', code: 'BOX-1' }, writeContext)
const createChild = createLocation({ name: 'Box', code: 'BOX-2', parent_id: 'parent' }, { ...writeContext, expectedRevision: 3 })
// @ts-expect-error Child creation cannot omit its reviewed parent revision.
const unprovenChild = createLocation({ name: 'Box', code: 'BOX-2', parent_id: 'parent' }, writeContext)
// @ts-expect-error Root creation cannot silently attach an unrelated If-Match.
const unrelatedRootProof = createLocation({ name: 'Box', code: 'BOX-1' }, { ...writeContext, expectedRevision: 3 })

void [createRoot, createChild, unprovenChild, unrelatedRootProof,
  invalidDelete, missingRevision, invalidQuantity, authHeaders, noOrigin, noCSRF, rawCredentialAdmin, hashLogin,
  childLocationHeaders, rootLocationHeaders, locationWithoutOrigin, rootLocation, minimalLocation, minimalItem, rootMove, parentWrapper, textSortOrder, nullKind]
