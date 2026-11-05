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

type CategoryCreateHeaders = operations['post_categories']['parameters']['header']
const rootCategoryHeaders = { ...authHeaders } satisfies CategoryCreateHeaders
const childCategoryHeaders = { ...authHeaders, 'If-Match': '"3"' } satisfies CategoryCreateHeaders
const categoryCreate = { name: 'Category' } satisfies components['schemas']['CategoryCreate']
const categoryMove = { parent_id: null } satisfies components['schemas']['CategoryWrite']
const categoryRename = { name: 'Renamed' } satisfies components['schemas']['CategoryWrite']
const category = { id: 'category', name: 'Category', parent_id: null, revision: 1 } satisfies components['schemas']['Category']
const tag = { name: 'Tag' } satisfies components['schemas']['TagWrite']
// @ts-expect-error Creating a category must include a name, unlike a nullable-parent-only patch.
const missingCategoryName: components['schemas']['CategoryCreate'] = { parent_id: null }
// @ts-expect-error Category responses always include the explicit nullable parent.
const missingCategoryParent: components['schemas']['Category'] = { id: 'category', name: 'Category', revision: 1 }
// @ts-expect-error Internal parent presence wrappers are not wire objects.
const categoryParentWrapper: components['schemas']['CategoryWrite'] = { parent_id: { set: true, value: null } }
// @ts-expect-error Classification writes require Origin independently of CSRF.
const missingCategoryOrigin: CategoryCreateHeaders = { 'X-CSRF-Token': 'csrf-fixture', 'Idempotency-Key': 'category-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error Tag names are not nullable.
const nullTag: components['schemas']['TagWrite'] = { name: null }

declare const createCategory: typeof import('../src/lib/api/taxonomy').createCategory
declare const createTag: typeof import('../src/lib/api/taxonomy').createTag
declare const patchCategory: typeof import('../src/lib/api/taxonomy').patchCategory
const rootCategory = createCategory({ name: 'Category' }, writeContext)
const childCategory = createCategory({ name: 'Child', parent_id: 'parent' }, { ...writeContext, expectedRevision: 3 })
const createNamedTag = createTag({ name: 'Tag' }, writeContext)
const patchCategoryParent = patchCategory('category', { parent_id: null }, { ...writeContext, expectedRevision: 1 })
// @ts-expect-error Child categories require the original reviewed parent proof.
const unprovenCategoryChild = createCategory({ name: 'Child', parent_id: 'parent' }, writeContext)
// @ts-expect-error Root categories cannot attach an unrelated aggregate proof.
const unrelatedCategoryProof = createCategory({ name: 'Root' }, { ...writeContext, expectedRevision: 1 })
// @ts-expect-error Tag creation has no parent/target aggregate precondition.
const unrelatedTagProof = createTag({ name: 'Tag' }, { ...writeContext, expectedRevision: 1 })
// @ts-expect-error Patches require an original target proof even for root moves.
const unprovenCategoryMove = patchCategory('category', { parent_id: null }, writeContext)

type ItemCreateHeaders = operations['post_items']['parameters']['header']
type ItemPatchHeaders = operations['patch_items_id']['parameters']['header']
const itemHeaders = { ...authHeaders } satisfies ItemCreateHeaders
const itemProof = { ...authHeaders, 'If-Match': '"3"' } satisfies ItemPatchHeaders
const measuredItem = { name: 'Stock', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } satisfies components['schemas']['ItemCreate']
const clearItem = { brand: null, model: null, category_id: null } satisfies components['schemas']['ItemWrite']
const restoreItem = { status: 'active' } satisfies components['schemas']['ItemWrite']
const emptyItemPage = { data: [], next_cursor: null } satisfies components['schemas']['ItemList']
const currentItem = {
  id: 'item', revision: 1, name: 'Item', brand: null, model: null, category_id: null,
  description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active',
} satisfies components['schemas']['Item']
// @ts-expect-error Catalog creation requires Origin independently of CSRF.
const itemWithoutOrigin: ItemCreateHeaders = { 'X-CSRF-Token': 'csrf-fixture', 'Idempotency-Key': 'item-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error Catalog editing requires the original aggregate proof.
const itemWithoutProof: ItemPatchHeaders = { ...authHeaders }
// @ts-expect-error Nullable brand/model/category are scalar wire fields, not internal wrappers.
const wrappedItem: components['schemas']['ItemWrite'] = { brand: { set: true, value: null } }
// @ts-expect-error Only brand/model/category are nullable; measurement cannot become null.
const nullItemScale: components['schemas']['ItemWrite'] = { scale_factor: null }
// @ts-expect-error Archived catalog metadata is restored explicitly, not with an invented deleted state.
const deletedItem: components['schemas']['ItemWrite'] = { status: 'deleted' }
// @ts-expect-error Catalog metadata cannot create or mutate holdings implicitly.
const implicitItemStock: components['schemas']['ItemCreate'] = { name: 'Item', quantity_base: 1 }
// @ts-expect-error Every live item page explicitly indicates whether another page exists.
const incompleteItemPage: components['schemas']['ItemList'] = { data: [] }

void [itemHeaders, itemProof, measuredItem, clearItem, restoreItem, emptyItemPage, currentItem,
  itemWithoutOrigin, itemWithoutProof, wrappedItem, nullItemScale, deletedItem, implicitItemStock, incompleteItemPage,
  rootCategory, childCategory, createNamedTag, patchCategoryParent, unprovenCategoryChild, unrelatedCategoryProof, unrelatedTagProof, unprovenCategoryMove,
  rootCategoryHeaders, childCategoryHeaders, categoryCreate, categoryMove, categoryRename, category, tag,
  missingCategoryName, missingCategoryParent, categoryParentWrapper, missingCategoryOrigin, nullTag,
  createRoot, createChild, unprovenChild, unrelatedRootProof,
  invalidDelete, missingRevision, invalidQuantity, authHeaders, noOrigin, noCSRF, rawCredentialAdmin, hashLogin,
  childLocationHeaders, rootLocationHeaders, locationWithoutOrigin, rootLocation, minimalLocation, minimalItem, rootMove, parentWrapper, textSortOrder, nullKind]
