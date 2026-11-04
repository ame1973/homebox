import type { components } from '../src/lib/api/schema'

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

void [invalidDelete, missingRevision, invalidQuantity]
