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

declare const createCatalogItem: typeof import('../src/lib/api/catalog').createItem
declare const patchCatalogItem: typeof import('../src/lib/api/catalog').patchItem
declare const archiveCatalogItem: typeof import('../src/lib/api/catalog').archiveItem
declare const deleteCatalogItem: typeof import('../src/lib/api/catalog').deleteItem
declare const fetchCatalogPage: typeof import('../src/lib/api/catalog').fetchItemsPage
const createMinimalCatalog = createCatalogItem({ name: 'Item' }, writeContext)
const createDefaultSubset = createCatalogItem({ name: 'Item', unit_code: 'pcs' }, writeContext)
const createMeasuredCatalog = createCatalogItem(measuredItem, writeContext)
const createClassifiedCatalog = createCatalogItem({ name: 'Item', category_id: 'category' }, writeContext)
const patchCatalogNull = patchCatalogItem('item', clearItem, { ...writeContext, expectedRevision: 1 })
const archiveCatalog = archiveCatalogItem('item', {}, { ...writeContext, expectedRevision: 1 })
const deleteCatalog = deleteCatalogItem('item', { ...writeContext, expectedRevision: 1 })
const readFilteredCatalog = fetchCatalogPage({ from: '2026-01-01', to: '2026-12-31', currency: 'TWD', category_id: 'category', location_id: 'location',
  scope: 'subtree', status: 'active', merchant: 'Merchant', q: 'Query', limit: 50 }, new AbortController().signal)
// @ts-expect-error Nondefault measurements need the complete explicit tuple, even though the generator ignores if/then.
const incompleteCatalogUnit = createCatalogItem({ name: 'Item', unit_code: 'kg' }, writeContext)
// @ts-expect-error A nondefault base unit cannot silently inherit display/scale defaults.
const incompleteCatalogBase = createCatalogItem({ name: 'Item', base_unit: 'g' }, writeContext)
// @ts-expect-error A nondefault scale cannot silently infer either unit.
const incompleteCatalogScale = createCatalogItem({ name: 'Item', scale_factor: 1000 }, writeContext)
// @ts-expect-error Item creation never attaches an unrelated parent/target proof, including when a category is selected.
const unrelatedCatalogProof = createCatalogItem({ name: 'Item', category_id: 'category' }, { ...writeContext, expectedRevision: 1 })
// @ts-expect-error Partial catalog edits require the reviewed original resource proof.
const unprovenCatalogPatch = patchCatalogItem('item', { name: 'New' }, writeContext)
// @ts-expect-error Archival requires the original item proof.
const unprovenCatalogArchive = archiveCatalogItem('item', {}, writeContext)
// @ts-expect-error Deletion requires the original item proof even for historical reconciliation.
const unprovenCatalogDelete = deleteCatalogItem('item', writeContext)
// @ts-expect-error Holdings are not implicit catalog metadata fields.
const implicitCatalogQuantity = createCatalogItem({ name: 'Item', quantity_base: 1 }, writeContext)
// @ts-expect-error The catalog status filter has only contracted metadata states.
const invalidCatalogStatus = fetchCatalogPage({ status: 'disposed' }, new AbortController().signal)

type BarcodeCreateHeaders = operations['post_items_id_barcodes']['parameters']['header']
const ownedCreateHeaders = { ...authHeaders, 'If-Match': '"3"' } satisfies BarcodeCreateHeaders
const barcodeBody = { symbology: 'UPC_A', raw_value: '036000291452' } satisfies components['schemas']['BarcodeWrite']
const aliasBody = { alias: '  Exact é  ' } satisfies components['schemas']['AliasWrite']
const aliasResource = { id: 'alias', item_id: 'item', revision: 1, alias: '  Exact é  ' } satisfies components['schemas']['Alias']
const attributeBody = { attribute_key: 'Specification', attribute_value: '' } satisfies components['schemas']['AttributeWrite']
const attributePartial = { attribute_value: '' } satisfies components['schemas']['AttributePatch']
const tagMembershipPage = { data: [], next_cursor: null } satisfies components['schemas']['ItemTagList']
const clearMembership = { tag_ids: [] } satisfies components['schemas']['ItemTagsInput']
const tagReplacementResult = currentItem satisfies operations['put_items_id_tags']['responses'][200]['content']['application/json']
// @ts-expect-error Owned creation requires exact Origin independently of its reviewed owner proof.
const missingOwnedOrigin: BarcodeCreateHeaders = { 'X-CSRF-Token': 'csrf', 'Idempotency-Key': 'owned-key', 'X-HomeBox-Client-Version': '0.1.0-dev', 'If-Match': '"3"' }
// @ts-expect-error Product barcode digits remain strings, never numeric coercions that lose zeros.
const numericBarcode: components['schemas']['BarcodeWrite'] = { symbology: 'UPC_A', raw_value: 36000291452 }
// @ts-expect-error Authoritative normalization is not a writable field.
const callerNormalizedBarcode: components['schemas']['BarcodeWrite'] = { ...barcodeBody, normalized_value: 'caller' }
// @ts-expect-error Stable alias ownership cannot be reassigned through a write.
const reassignedAlias: components['schemas']['AliasWrite'] = { alias: 'Alias', item_id: 'other' }
// @ts-expect-error Every alias response includes the actual alias text.
const incompleteAlias: components['schemas']['Alias'] = { id: 'alias', item_id: 'item', revision: 1 }
// @ts-expect-error Attribute creation requires an explicit value even when it is empty.
const incompleteAttribute: components['schemas']['AttributeWrite'] = { attribute_key: 'Key' }
// @ts-expect-error Empty attribute text is represented by a string, not null.
const nullAttribute: components['schemas']['AttributePatch'] = { attribute_value: null }
// @ts-expect-error Every item-membership page explicitly terminates or continues.
const incompleteMembershipPage: components['schemas']['ItemTagList'] = { data: [] }
// @ts-expect-error Tag replacement returns the frozen owning Item, not a partial TagList.
const partialMembershipResult: operations['put_items_id_tags']['responses'][200]['content']['application/json'] = { data: [], next_cursor: null }

declare const ownedCatalogAPI: typeof import('../src/lib/api/catalog-links')
const owningItemProof = { ...writeContext, expectedRevision: 7 }
const independentRowProof = { ...writeContext, expectedRevision: 2, itemID: 'item' }
const ownedSDKCalls = [
  ownedCatalogAPI.fetchItemBarcodesPage('item', { limit: 50 }, new AbortController().signal),
  ownedCatalogAPI.fetchItemAliasesPage('item', {}, new AbortController().signal),
  ownedCatalogAPI.fetchItemAttributesPage('item', {}, new AbortController().signal),
  ownedCatalogAPI.fetchItemTagsPage('item', {}, new AbortController().signal),
  ownedCatalogAPI.createBarcode('item', barcodeBody, owningItemProof),
  ownedCatalogAPI.createAlias('item', aliasBody, owningItemProof),
  ownedCatalogAPI.createAttribute('item', attributeBody, owningItemProof),
  ownedCatalogAPI.patchBarcode('barcode', barcodeBody, independentRowProof),
  ownedCatalogAPI.patchAlias('alias', aliasBody, independentRowProof),
  ownedCatalogAPI.patchAttribute('attribute', attributePartial, independentRowProof),
  ownedCatalogAPI.deleteBarcode('barcode', independentRowProof),
  ownedCatalogAPI.deleteAlias('alias', independentRowProof),
  ownedCatalogAPI.deleteAttribute('attribute', independentRowProof),
  ownedCatalogAPI.replaceItemTags('item', clearMembership, owningItemProof),
]
// @ts-expect-error Owned creation requires the reviewed original Item revision.
const unprovenOwnedBarcode = ownedCatalogAPI.createBarcode('item', barcodeBody, writeContext)
// @ts-expect-error Alias creation also requires the Item proof, unlike global Tag creation.
const unprovenOwnedAlias = ownedCatalogAPI.createAlias('item', aliasBody, writeContext)
// @ts-expect-error Attribute creation cannot silently adopt a current Item revision.
const unprovenOwnedAttribute = ownedCatalogAPI.createAttribute('item', attributeBody, writeContext)
// @ts-expect-error Independent row replies must validate the retained original owner too.
const ownerlessBarcodePatch = ownedCatalogAPI.patchBarcode('barcode', barcodeBody, owningItemProof)
// @ts-expect-error A row deletion context is not an unscoped Item-proof context.
const ownerlessAliasDelete = ownedCatalogAPI.deleteAlias('alias', owningItemProof)
// @ts-expect-error Empty attribute patches are rejected at compile time and runtime.
const emptyOwnedAttributePatch = ownedCatalogAPI.patchAttribute('attribute', {}, independentRowProof)
// @ts-expect-error Attribute creation requires a possibly-empty explicit value.
const missingOwnedAttributeValue = ownedCatalogAPI.createAttribute('item', { attribute_key: 'Key' }, owningItemProof)
// @ts-expect-error Authoritative normalization is never a submitted SDK field.
const normalizedOwnedInput = ownedCatalogAPI.createBarcode('item', { ...barcodeBody, normalized_value: 'caller' }, owningItemProof)
// @ts-expect-error Stable owner identity cannot be reassigned through an alias patch.
const reassignedOwnedInput = ownedCatalogAPI.patchAlias('alias', { ...aliasBody, item_id: 'other' }, independentRowProof)
// @ts-expect-error Complete tag replacement needs the original owning Item proof.
const unprovenTagSet = ownedCatalogAPI.replaceItemTags('item', clearMembership, writeContext)
// @ts-expect-error Null is not the explicit empty membership array.
const nullTagSet = ownedCatalogAPI.replaceItemTags('item', { tag_ids: null }, owningItemProof)
// @ts-expect-error Assigned reads accept only their documented paging fields.
const filteredOwnedRead = ownedCatalogAPI.fetchItemTagsPage('item', { q: 'uncontracted' }, new AbortController().signal)
type UnitCreateHeaders = operations['post_items_id_units']['parameters']['header']
type UnitEditHeaders = operations['patch_units_id']['parameters']['header']
type StockMutationHeaders = operations['post_stock_receive']['parameters']['header']
type UnitDeletion = operations['delete_units_id']['responses'][200]['content']['application/json']
const reviewedHoldingItem = { ...authHeaders, 'If-Match': '"7"' } satisfies UnitCreateHeaders & StockMutationHeaders
const reviewedHoldingUnit = { ...authHeaders, 'If-Match': '"3"' } satisfies UnitEditHeaders
const unitWithUnknownLocation = { location_id: null } satisfies components['schemas']['UnitCreate']
const unitWithEmptySerial = { location_id: 'location', serial_number: '', notes: '', status: 'stored' } satisfies components['schemas']['UnitCreate']
const unitSerialClear = { serial_number: null } satisfies components['schemas']['UnitWrite']
const unitNotesClear = { notes: '' } satisfies components['schemas']['UnitWrite']
const unitMove = { target_location_id: 'destination' } satisfies components['schemas']['UnitMove']
const unitStatus = { status: 'lost', note: 'Explicitly reviewed' } satisfies components['schemas']['UnitStatusInput']
const physicalUnitDisposal = { status: 'disposed', disposal_kind: 'discarded', note: 'Explicit non-asset action' } satisfies components['schemas']['UnitStatusInput']
// @ts-expect-error Non-asset disposal must explicitly distinguish its physical action.
const untypedUnitDisposal: components['schemas']['UnitStatusInput'] = { status: 'disposed' }
// @ts-expect-error An operational state cannot silently reclassify historical disposal.
const operationalDisposalKind: components['schemas']['UnitStatusInput'] = { status: 'stored', disposal_kind: 'discarded' }
// @ts-expect-error Financial proceeds require the separate asset lifecycle command.
const automaticUnitProceeds: components['schemas']['UnitStatusInput'] = { status: 'disposed', disposal_kind: 'sold', realized_proceeds_minor: 1 }
// @ts-expect-error New physical disposal requires one of the explicit supported actions.
const unknownUnitDisposal: components['schemas']['UnitStatusInput'] = { status: 'disposed', disposal_kind: 'other' }
void [physicalUnitDisposal, untypedUnitDisposal, operationalDisposalKind, automaticUnitProceeds, unknownUnitDisposal]
const holdingUnit = { id: 'unit', revision: 3, item_id: 'item', location_id: null, asset_code: 'AST-001', serial_number: null, notes: '', status: 'stored' } satisfies components['schemas']['Unit']
const holdingStock = { item_id: 'item', location_id: 'location', revision: 2, quantity_base: 1500, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } satisfies components['schemas']['Stock']
const receiveStock = { item_id: 'item', target_location_id: 'location', quantity_base: 1000 } satisfies components['schemas']['StockReceiveInput']
const consumeStock = { item_id: 'item', source_location_id: 'location', quantity_base: 1 } satisfies components['schemas']['StockConsumeInput']
const transferStock = { item_id: 'item', source_location_id: 'source', target_location_id: 'target', quantity_base: 1 } satisfies components['schemas']['StockTransferInput']
const adjustStockToZero = { item_id: 'item', target_location_id: 'location', quantity_base: 0, reason: 'Explicit count' } satisfies components['schemas']['StockAdjustInput']
const emptyUnitPage = { data: [], next_cursor: null } satisfies components['schemas']['UnitList']
const emptyStockPage = { data: [], next_cursor: null } satisfies components['schemas']['StockList']
const deletedUnit = { ok: true, operation_id: 'event', revision: 4 } satisfies UnitDeletion
// @ts-expect-error The location decision is explicit even when it is null.
const unitWithoutLocation: components['schemas']['UnitCreate'] = { serial_number: '' }
// @ts-expect-error The immutable asset code is assigned by the authoritative service.
const callerUnitCode: components['schemas']['UnitCreate'] = { location_id: null, asset_code: 'caller' }
// @ts-expect-error New units cannot start with a terminal lifecycle state.
const terminalUnitCreate: components['schemas']['UnitCreate'] = { location_id: null, status: 'retired' }
// @ts-expect-error Only serial text is nullable; notes are exact nonnullable text.
const nullUnitNotes: components['schemas']['UnitWrite'] = { notes: null }
// @ts-expect-error Serial numbers are strings, never numeric coercions.
const numericUnitSerial: components['schemas']['UnitWrite'] = { serial_number: 123 }
// @ts-expect-error Owning Item identity is immutable on a unit metadata patch.
const reassignedUnit: components['schemas']['UnitWrite'] = { item_id: 'other' }
// @ts-expect-error Moving a unit is a separate original-unit-proof command.
const patchUnitLocation: components['schemas']['UnitWrite'] = { location_id: 'other' }
// @ts-expect-error Lifecycle transitions are not ordinary metadata patches.
const patchUnitStatus: components['schemas']['UnitWrite'] = { status: 'disposed' }
// @ts-expect-error A unit location is cleared only by explicit supported domain workflows, not a null move target.
const nullUnitMove: components['schemas']['UnitMove'] = { target_location_id: null }
// @ts-expect-error Quantity stock always uses integer-valued JSON numbers, not text.
const textHoldingQuantity: components['schemas']['StockReceiveInput'] = { item_id: 'item', target_location_id: 'target', quantity_base: '1000' }
// @ts-expect-error A transfer must identify both original locations.
const incompleteStockTransfer: components['schemas']['StockTransferInput'] = { item_id: 'item', target_location_id: 'target', quantity_base: 1 }
// @ts-expect-error Absolute adjustment requires an explicitly reviewed reason.
const reasonlessStockAdjustment: components['schemas']['StockAdjustInput'] = { item_id: 'item', target_location_id: 'target', quantity_base: 0 }
// @ts-expect-error Stock writes never install a caller-supplied measurement conversion.
const implicitStockConversion: components['schemas']['StockReceiveInput'] = { ...receiveStock, scale_factor: 1000 }
// @ts-expect-error A read balance must include its actual complete Item measurement tuple.
const incompleteStockMeasurement: components['schemas']['Stock'] = { item_id: 'item', location_id: 'location', revision: 1, quantity_base: 1 }
// @ts-expect-error Unit creation requires the original owning Item proof.
const unprovenHoldingCreation: UnitCreateHeaders = { ...authHeaders }
// @ts-expect-error Existing unit edits require their independent original Unit proof.
const unprovenHoldingEdit: UnitEditHeaders = { ...authHeaders }
// @ts-expect-error Stock mutation requires the original Item proof, not an implicit latest revision.
const unprovenStockMutation: StockMutationHeaders = { ...authHeaders }
// @ts-expect-error Same-origin protected unit writes require Origin in addition to the proof.
const originlessHoldingEdit: UnitEditHeaders = { 'If-Match': '"3"', 'X-CSRF-Token': 'csrf', 'Idempotency-Key': 'key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error A complete unit page must explicitly terminate or continue.
const incompleteUnitPage: components['schemas']['UnitList'] = { data: [] }
// @ts-expect-error A complete inventory page must explicitly terminate or continue.
const incompleteStockPage: components['schemas']['StockList'] = { data: [] }
// @ts-expect-error Unit deletion acknowledges its independent retained next revision.
const unversionedUnitDeletion: UnitDeletion = { ok: true, operation_id: 'event' }
void [reviewedHoldingItem, reviewedHoldingUnit, unitWithUnknownLocation, unitWithEmptySerial, unitSerialClear, unitNotesClear, unitMove, unitStatus,
  holdingUnit, holdingStock, receiveStock, consumeStock, transferStock, adjustStockToZero, emptyUnitPage, emptyStockPage, deletedUnit,
  unitWithoutLocation, callerUnitCode, terminalUnitCreate, nullUnitNotes, numericUnitSerial, reassignedUnit, patchUnitLocation, patchUnitStatus,
  nullUnitMove, textHoldingQuantity, incompleteStockTransfer, reasonlessStockAdjustment, implicitStockConversion, incompleteStockMeasurement,
  unprovenHoldingCreation, unprovenHoldingEdit, unprovenStockMutation, originlessHoldingEdit, incompleteUnitPage, incompleteStockPage, unversionedUnitDeletion]

type StockReadQuery = NonNullable<operations['get_inventory']['parameters']['query']>
const unfilteredStockRead = {} satisfies StockReadQuery
const filteredStockRead = { item_id: 'item', from: '2026-11-05', to: '2026-11-06', currency: 'TWD', category_id: 'category', location_id: 'location', scope: 'subtree', status: 'archived', merchant: 'Exact merchant', limit: 50, cursor: 'opaque' } satisfies StockReadQuery
// @ts-expect-error Inventory status describes Item metadata, not an individual Unit lifecycle.
const lifecycleStockRead: StockReadQuery = { status: 'stored' }
// @ts-expect-error Inventory has no catalog q parameter or implicit text-search mode.
const searchedStockRead: StockReadQuery = { q: 'uncontracted' }
// @ts-expect-error Query adapters must preserve a numeric page size.
const textStockReadSize: StockReadQuery = { limit: '50' }
// @ts-expect-error An omitted category filter is distinct from a null query field.
const nullStockReadCategory: StockReadQuery = { category_id: null }
const itemScopedStockRead = { item_id: 'item', limit: 1 } satisfies StockReadQuery
// @ts-expect-error Item selection is exact string identity, not an array of matches.
const multipleStockItemIDs: StockReadQuery = { item_id: ['item', 'other'] }
// @ts-expect-error An omitted Item criterion is distinct from a null query value.
const nullStockItemID: StockReadQuery = { item_id: null }
// @ts-expect-error The additive inventory criterion does not widen catalog queries.
const catalogItemIDFilter = fetchCatalogPage({ item_id: 'item' }, new AbortController().signal)
void [itemScopedStockRead, multipleStockItemIDs, nullStockItemID, catalogItemIDFilter]
void [unfilteredStockRead, filteredStockRead, lifecycleStockRead, searchedStockRead, textStockReadSize, nullStockReadCategory]

declare const holdingSDK: typeof import('../src/lib/api/holdings')
const independentUnitContext = { ...writeContext, itemID: currentItem.id, expectedRevision: holdingUnit.revision }
const reviewedQuantityItem = { ...currentItem, tracking_mode: 'quantity' as const, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const reviewedStockContext = { ...writeContext, item: reviewedQuantityItem }
const holdingSDKCalls = [
  holdingSDK.fetchUnit(holdingUnit.id, new AbortController().signal),
  holdingSDK.fetchItemUnitsPage(currentItem.id, { limit: 50 }, new AbortController().signal),
  holdingSDK.fetchStockPage(filteredStockRead, new AbortController().signal),
  holdingSDK.createUnit(currentItem.id, unitWithUnknownLocation, owningItemProof),
  holdingSDK.patchUnit(holdingUnit.id, unitSerialClear, independentUnitContext),
  holdingSDK.patchUnit(holdingUnit.id, unitNotesClear, independentUnitContext),
  holdingSDK.moveUnit(holdingUnit.id, unitMove, independentUnitContext),
  holdingSDK.setUnitStatus(holdingUnit.id, unitStatus, independentUnitContext),
  holdingSDK.setUnitStatus(holdingUnit.id, physicalUnitDisposal, independentUnitContext),
  holdingSDK.deleteUnit(holdingUnit.id, independentUnitContext),
  holdingSDK.receiveStock(receiveStock, reviewedStockContext),
  holdingSDK.consumeStock(consumeStock, reviewedStockContext),
  holdingSDK.transferStock(transferStock, reviewedStockContext),
  holdingSDK.adjustStock(adjustStockToZero, reviewedStockContext),
]
// @ts-expect-error Unit creation requires an explicit nullable location decision.
const implicitSDKLocation = holdingSDK.createUnit(currentItem.id, {}, owningItemProof)
// @ts-expect-error Unit creation requires the original Item proof.
const unprovenSDKUnit = holdingSDK.createUnit(currentItem.id, unitWithUnknownLocation, writeContext)
// @ts-expect-error Metadata editing requires at least one explicit field.
const emptySDKUnitPatch = holdingSDK.patchUnit(holdingUnit.id, {}, independentUnitContext)
// @ts-expect-error Independent Unit edits retain their original owner identity.
const ownerlessSDKUnitPatch = holdingSDK.patchUnit(holdingUnit.id, unitSerialClear, owningItemProof)
// @ts-expect-error Independent Unit deletion also retains the original owner.
const ownerlessSDKUnitDelete = holdingSDK.deleteUnit(holdingUnit.id, owningItemProof)
// @ts-expect-error Metadata editing cannot change physical location.
const implicitSDKUnitMove = holdingSDK.patchUnit(holdingUnit.id, { location_id: 'target' }, independentUnitContext)
// @ts-expect-error Physical disposal has an explicit discriminated action.
const ambiguousSDKDisposal = holdingSDK.setUnitStatus(holdingUnit.id, { status: 'disposed' }, independentUnitContext)
// @ts-expect-error Financial lifecycle input cannot bypass the asset command.
const financialSDKDisposal = holdingSDK.setUnitStatus(holdingUnit.id, { status: 'disposed', disposal_kind: 'sold', realized_proceeds_minor: 1 }, independentUnitContext)
// @ts-expect-error Stock proofs and measurement come from one whole reviewed Item, not a loose row revision.
const looseSDKStockProof = holdingSDK.receiveStock(receiveStock, owningItemProof)
// @ts-expect-error A Stock row cannot stand in for the reviewed authoritative Item.
const stockAsSDKItem = holdingSDK.receiveStock(receiveStock, { ...writeContext, item: holdingStock })
// @ts-expect-error An independently supplied proof cannot override the reviewed Item revision.
const overriddenSDKStockProof = holdingSDK.receiveStock(receiveStock, { ...reviewedStockContext, expectedRevision: 2 })
// @ts-expect-error Stock requests cannot silently install measurement conversion fields.
const convertedSDKStock = holdingSDK.receiveStock({ ...receiveStock, scale_factor: 1000 }, reviewedStockContext)
// @ts-expect-error Transfers require the actual source as well as destination.
const incompleteSDKTransfer = holdingSDK.transferStock(receiveStock, reviewedStockContext)
// @ts-expect-error An absolute physical adjustment always needs an explicit reason.
const reasonlessSDKAdjust = holdingSDK.adjustStock({ ...receiveStock, quantity_base: 0 }, reviewedStockContext)
// @ts-expect-error Unit pages accept only their collection paging fields.
const filteredSDKUnits = holdingSDK.fetchItemUnitsPage(currentItem.id, { status: 'stored' }, new AbortController().signal)
// @ts-expect-error Stock reads do not invent a text-search query.
const searchedSDKStock = holdingSDK.fetchStockPage({ q: 'uncontracted' }, new AbortController().signal)
void [holdingSDKCalls, implicitSDKLocation, unprovenSDKUnit, emptySDKUnitPatch, ownerlessSDKUnitPatch, ownerlessSDKUnitDelete, implicitSDKUnitMove,
  ambiguousSDKDisposal, financialSDKDisposal, looseSDKStockProof, stockAsSDKItem, overriddenSDKStockProof, convertedSDKStock, incompleteSDKTransfer,
  reasonlessSDKAdjust, filteredSDKUnits, searchedSDKStock]

type PurchaseCreateHeaders = operations['post_purchases']['parameters']['header']
type PurchaseEditHeaders = operations['patch_purchases_id']['parameters']['header']
type PurchaseLineHeaders = operations['post_purchases_id_lines']['parameters']['header']
type PurchaseQuery = NonNullable<operations['get_purchases']['parameters']['query']>
type PurchaseLineQuery = NonNullable<operations['get_purchases_id_lines']['parameters']['query']>
const purchaseHeaders = { ...authHeaders } satisfies PurchaseCreateHeaders
const purchaseProof = { ...authHeaders, 'If-Match': '"7"' } satisfies PurchaseEditHeaders & PurchaseLineHeaders
const purchaseHeader = { merchant_name: '  Merchant é  ', purchased_at: '2026-11-06T00:00:00Z', currency: 'JPY' } satisfies components['schemas']['PurchaseWrite']
const purchaseReceiptClear = { receipt_note: '' } satisfies components['schemas']['PurchasePatch']
const unallocatedPurchaseLine = { item_id: 'item', item_revision: 7, item_unit_id: null, quantity_base: 1500, paid_minor: 0 } satisfies components['schemas']['PurchaseLineWrite']
const allocatedPurchaseLine = { item_id: 'item', item_revision: 7, item_unit_id: 'unit', quantity_base: 1, paid_minor: 120000 } satisfies components['schemas']['PurchaseLineWrite']
const completePurchasePage = { data: [], next_cursor: null } satisfies components['schemas']['PurchaseList']
const completePurchaseLinePage = { data: [], next_cursor: null } satisfies components['schemas']['PurchaseLineList']
const filteredPurchases = { from: '2026-01-01', to: '2026-11-06', currency: 'JPY', category_id: 'category', location_id: 'location', scope: 'direct', status: 'active', merchant: 'Exact merchant', limit: 50, cursor: 'opaque' } satisfies PurchaseQuery
// @ts-expect-error Purchase writes require exact Origin independently of CSRF.
const originlessPurchase: PurchaseCreateHeaders = { 'X-CSRF-Token': 'csrf', 'Idempotency-Key': 'purchase-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error Descriptive corrections require the original Purchase proof.
const unprovenPurchase: PurchaseEditHeaders = { ...authHeaders }
// @ts-expect-error Allocation creation uses the original Purchase proof too.
const unprovenPurchaseLine: PurchaseLineHeaders = { ...authHeaders }
// @ts-expect-error A new header has no unrelated Item or parent proof.
const unrelatedPurchaseProof: PurchaseCreateHeaders = { ...authHeaders, 'If-Match': '"3"' }
// @ts-expect-error Header metadata never counts the same paid amount a second time.
const headerPurchaseAmount: components['schemas']['PurchaseWrite'] = { ...purchaseHeader, paid_minor: 120000 }
// @ts-expect-error Nullable Unit allocation intent is explicit, not an omitted default.
const implicitPurchaseAllocation: components['schemas']['PurchaseLineWrite'] = { item_id: 'item', item_revision: 7, quantity_base: 1, paid_minor: 0 }
// @ts-expect-error One named physical Unit cannot silently represent a bulk price multiplier.
const bulkUnitPurchaseLine: components['schemas']['PurchaseLineWrite'] = { item_id: 'item', item_revision: 7, item_unit_id: 'unit', quantity_base: 2, paid_minor: 120000 }
// @ts-expect-error The original Purchase proof cannot substitute for the reviewed Item interpretation.
const unprovenPurchaseItem: components['schemas']['PurchaseLineWrite'] = { item_id: 'item', item_unit_id: null, quantity_base: 1500, paid_minor: 0 }
void unprovenPurchaseItem
// @ts-expect-error The complete minor-unit amount is a JSON number, not decimal display text.
const textPurchaseAmount: components['schemas']['PurchaseLineWrite'] = { ...unallocatedPurchaseLine, paid_minor: '1200' }
// @ts-expect-error Receipt text is nonnullable; empty clears it explicitly.
const nullPurchaseReceipt: components['schemas']['PurchasePatch'] = { receipt_note: null }
// @ts-expect-error Purchase pages explicitly continue or terminate.
const incompletePurchasePage: components['schemas']['PurchaseList'] = { data: [] }
// @ts-expect-error Allocation pages explicitly continue or terminate too.
const incompletePurchaseLinePage: components['schemas']['PurchaseLineList'] = { data: [] }
// @ts-expect-error The Purchase owner comes from the path, not the allocation body.
const reassignedPurchaseLine: components['schemas']['PurchaseLineWrite'] = { ...unallocatedPurchaseLine, purchase_id: 'other' }
// @ts-expect-error Purchase status filters describe current Item metadata, not Unit lifecycle.
const lifecyclePurchaseQuery: PurchaseQuery = { status: 'disposed' }
// @ts-expect-error The selected Purchase line collection accepts only paging fields.
const filteredPurchaseLines: PurchaseLineQuery = { currency: 'JPY' }
declare const purchaseSDK: typeof import('../src/lib/api/purchases')
declare const purchaseTransport: typeof import('../src/lib/api/client').apiClient
const reviewedPurchase = { id: 'purchase', revision: 7, ...purchaseHeader, receipt_note: 'Retained' } satisfies components['schemas']['Purchase']
const purchaseSDKContext = { ...writeContext, purchase: reviewedPurchase }
const purchaseSDKLineContext = { ...purchaseSDKContext, item: { ...reviewedQuantityItem, revision: 7 } }
const purchaseSDKCalls = [
  purchaseSDK.fetchPurchase(reviewedPurchase.id, new AbortController().signal),
  purchaseSDK.fetchPurchasesPage(filteredPurchases, new AbortController().signal),
  purchaseSDK.fetchItemPurchasesPage(currentItem.id, { limit: 50 }, new AbortController().signal),
  purchaseSDK.fetchPurchaseLinesPage(reviewedPurchase.id, { limit: 50 }, new AbortController().signal),
  purchaseSDK.createPurchase(purchaseHeader, writeContext),
  purchaseSDK.patchPurchase(reviewedPurchase.id, purchaseReceiptClear, purchaseSDKContext),
  purchaseSDK.createPurchaseLine(reviewedPurchase.id, unallocatedPurchaseLine, purchaseSDKLineContext),
  purchaseSDK.createPurchaseLine(reviewedPurchase.id, allocatedPurchaseLine, { ...purchaseSDKContext, item: { ...currentItem, revision: 7 } }),
]
const rawNullPurchaseLine = purchaseTransport.POST('/purchases/{id}/lines', { params: { path: { id: reviewedPurchase.id }, header: purchaseProof }, body: unallocatedPurchaseLine })
const rawNamedPurchaseLine = purchaseTransport.POST('/purchases/{id}/lines', { params: { path: { id: reviewedPurchase.id }, header: purchaseProof }, body: allocatedPurchaseLine })
// @ts-expect-error A new header has no unrelated aggregate proof.
const unrelatedSDKPurchaseProof = purchaseSDK.createPurchase(purchaseHeader, { ...writeContext, expectedRevision: 7 })
// @ts-expect-error Header metadata cannot double count a paid total.
const totalSDKPurchaseHeader = purchaseSDK.createPurchase({ ...purchaseHeader, paid_minor: 120000 }, writeContext)
// @ts-expect-error Corrections retain the whole original Purchase, not a loose proof.
const looseSDKPurchaseProof = purchaseSDK.patchPurchase(reviewedPurchase.id, purchaseReceiptClear, { ...writeContext, expectedRevision: 7 })
// @ts-expect-error An independently refreshed revision cannot override the reviewed header.
const overriddenSDKPurchaseProof = purchaseSDK.patchPurchase(reviewedPurchase.id, purchaseReceiptClear, { ...purchaseSDKContext, expectedRevision: 7 })
// @ts-expect-error A descriptive correction includes at least one actual field.
const emptySDKPurchasePatch = purchaseSDK.patchPurchase(reviewedPurchase.id, {}, purchaseSDKContext)
// @ts-expect-error Receipt text clears through empty, never null.
const nullSDKPurchaseReceipt = purchaseSDK.patchPurchase(reviewedPurchase.id, { receipt_note: null }, purchaseSDKContext)
// @ts-expect-error A Line also retains the whole original Item interpretation.
const ownerlessSDKPurchaseLine = purchaseSDK.createPurchaseLine(reviewedPurchase.id, unallocatedPurchaseLine, purchaseSDKContext)
// @ts-expect-error A Stock row is not an Item interpretation.
const stockAsSDKPurchaseItem = purchaseSDK.createPurchaseLine(reviewedPurchase.id, unallocatedPurchaseLine, { ...purchaseSDKContext, item: holdingStock })
// @ts-expect-error The body still requires original Item revision separately from Purchase proof.
const unprovenSDKPurchaseItem = purchaseSDK.createPurchaseLine(reviewedPurchase.id, { item_id: 'item', item_unit_id: null, quantity_base: 1, paid_minor: 0 }, purchaseSDKLineContext)
// @ts-expect-error The nullable Unit decision cannot be omitted.
const implicitSDKPurchaseUnit = purchaseSDK.createPurchaseLine(reviewedPurchase.id, { item_id: 'item', item_revision: 7, quantity_base: 1, paid_minor: 0 }, purchaseSDKLineContext)
// @ts-expect-error One named Unit has quantity exactly one, not a bulk multiplier.
const bulkSDKPurchaseUnit = purchaseSDK.createPurchaseLine(reviewedPurchase.id, { ...allocatedPurchaseLine, quantity_base: 2 }, purchaseSDKLineContext)
// @ts-expect-error Money is the complete numeric minor-unit amount, not display text.
const textSDKPurchaseMoney = purchaseSDK.createPurchaseLine(reviewedPurchase.id, { ...unallocatedPurchaseLine, paid_minor: '120000' }, purchaseSDKLineContext)
// @ts-expect-error A Line cannot override header currency.
const currencySDKPurchaseLine = purchaseSDK.createPurchaseLine(reviewedPurchase.id, { ...unallocatedPurchaseLine, currency: 'USD' }, purchaseSDKLineContext)
// @ts-expect-error Purchase collections have no catalog text query.
const searchedSDKPurchases = purchaseSDK.fetchPurchasesPage({ q: 'uncontracted' }, new AbortController().signal)
// @ts-expect-error Owned headers do not silently accept global filters.
const filteredSDKItemPurchases = purchaseSDK.fetchItemPurchasesPage(currentItem.id, { currency: 'JPY' }, new AbortController().signal)
// @ts-expect-error Owned Lines also accept only their original collection paging scope.
const filteredSDKPurchaseLines = purchaseSDK.fetchPurchaseLinesPage(reviewedPurchase.id, { status: 'active' }, new AbortController().signal)
// @ts-expect-error The shared transport must still require explicit nullable presence.
const implicitRawPurchaseUnit = purchaseTransport.POST('/purchases/{id}/lines', { params: { path: { id: reviewedPurchase.id }, header: purchaseProof }, body: { item_id: 'item', item_revision: 7, quantity_base: 1, paid_minor: 0 } })
// @ts-expect-error Null preservation must not widen the named-Unit oneOf constraint.
const bulkRawPurchaseUnit = purchaseTransport.POST('/purchases/{id}/lines', { params: { path: { id: reviewedPurchase.id }, header: purchaseProof }, body: { ...allocatedPurchaseLine, quantity_base: 2 } })
// @ts-expect-error A type-only write marker is never an actual wire value.
const markedRawPurchaseUnit = purchaseTransport.POST('/purchases/{id}/lines', { params: { path: { id: reviewedPurchase.id }, header: purchaseProof }, body: { ...unallocatedPurchaseLine, item_unit_id: { $write: null } } })
void [purchaseSDKCalls, rawNullPurchaseLine, rawNamedPurchaseLine, unrelatedSDKPurchaseProof, totalSDKPurchaseHeader, looseSDKPurchaseProof,
  overriddenSDKPurchaseProof, emptySDKPurchasePatch, nullSDKPurchaseReceipt, ownerlessSDKPurchaseLine, stockAsSDKPurchaseItem, unprovenSDKPurchaseItem,
  implicitSDKPurchaseUnit, bulkSDKPurchaseUnit, textSDKPurchaseMoney, currencySDKPurchaseLine, searchedSDKPurchases, filteredSDKItemPurchases,
  filteredSDKPurchaseLines, implicitRawPurchaseUnit, bulkRawPurchaseUnit, markedRawPurchaseUnit]

void [purchaseHeaders, purchaseProof, purchaseHeader, purchaseReceiptClear, unallocatedPurchaseLine, allocatedPurchaseLine, completePurchasePage, completePurchaseLinePage,
  filteredPurchases, originlessPurchase, unprovenPurchase, unprovenPurchaseLine, unrelatedPurchaseProof, headerPurchaseAmount, implicitPurchaseAllocation,
  bulkUnitPurchaseLine, textPurchaseAmount, nullPurchaseReceipt, incompletePurchasePage, incompletePurchaseLinePage, reassignedPurchaseLine, lifecyclePurchaseQuery, filteredPurchaseLines]

void [ownedSDKCalls, unprovenOwnedBarcode, unprovenOwnedAlias, unprovenOwnedAttribute, ownerlessBarcodePatch, ownerlessAliasDelete, emptyOwnedAttributePatch,
  missingOwnedAttributeValue, normalizedOwnedInput, reassignedOwnedInput, unprovenTagSet, nullTagSet, filteredOwnedRead]

void [ownedCreateHeaders, barcodeBody, aliasBody, aliasResource, attributeBody, attributePartial, tagMembershipPage, clearMembership, tagReplacementResult,
  missingOwnedOrigin, numericBarcode, callerNormalizedBarcode, reassignedAlias, incompleteAlias, incompleteAttribute, nullAttribute, incompleteMembershipPage, partialMembershipResult,
  createMinimalCatalog, createDefaultSubset, createMeasuredCatalog, createClassifiedCatalog, patchCatalogNull, archiveCatalog, deleteCatalog, readFilteredCatalog,
  incompleteCatalogUnit, incompleteCatalogBase, incompleteCatalogScale, unrelatedCatalogProof, unprovenCatalogPatch, unprovenCatalogArchive, unprovenCatalogDelete,
  implicitCatalogQuantity, invalidCatalogStatus,
  itemHeaders, itemProof, measuredItem, clearItem, restoreItem, emptyItemPage, currentItem,
  itemWithoutOrigin, itemWithoutProof, wrappedItem, nullItemScale, deletedItem, implicitItemStock, incompleteItemPage,
  rootCategory, childCategory, createNamedTag, patchCategoryParent, unprovenCategoryChild, unrelatedCategoryProof, unrelatedTagProof, unprovenCategoryMove,
  rootCategoryHeaders, childCategoryHeaders, categoryCreate, categoryMove, categoryRename, category, tag,
  missingCategoryName, missingCategoryParent, categoryParentWrapper, missingCategoryOrigin, nullTag,
  createRoot, createChild, unprovenChild, unrelatedRootProof,
  invalidDelete, missingRevision, invalidQuantity, authHeaders, noOrigin, noCSRF, rawCredentialAdmin, hashLogin,
  childLocationHeaders, rootLocationHeaders, locationWithoutOrigin, rootLocation, minimalLocation, minimalItem, rootMove, parentWrapper, textSortOrder, nullKind]
