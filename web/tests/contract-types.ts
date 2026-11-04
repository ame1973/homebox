import type { components, operations } from '../src/lib/api/schema'

type Record = components['schemas']['OfflineRecord']

const stock = {
  entity_type: 'stock', entity_id: 'item-1:location-1', entity_revision: 1,
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

void [invalidDelete, missingRevision, invalidQuantity, authHeaders, noOrigin, noCSRF, rawCredentialAdmin, hashLogin]
